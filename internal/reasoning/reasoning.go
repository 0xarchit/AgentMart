// Package reasoning provides bounded purchase intent decisions.
package reasoning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// DecisionAction is the bounded intent selected by the reasoning boundary.
type DecisionAction string

const (
	// ActionBuy requests a straight-through purchase when the Gate permits it.
	ActionBuy DecisionAction = "buy"
	// ActionNegotiate requests a merchant counter offer.
	ActionNegotiate DecisionAction = "negotiate"
	// ActionAskHuman requests explicit user confirmation.
	ActionAskHuman DecisionAction = "ask_human"
	// ActionDecline rejects the request without a money operation.
	ActionDecline DecisionAction = "decline"
)

// Input contains the user request and trusted catalog facts available to reasoning.
type Input struct {
	Request          string `json:"request"`
	ProductID        string `json:"product_id"`
	ProductName      string `json:"product_name,omitempty"`
	Category         string `json:"category,omitempty"`
	Quantity         int    `json:"quantity"`
	Stock            int    `json:"stock"`
	WarrantyYears    int    `json:"warranty_years"`
	TrustScore       int    `json:"trust_score"`
	ComboWith        string `json:"combo_with,omitempty"`
	ComboDiscountPct int    `json:"combo_discount_pct"`
	OfferReason      string `json:"offer_reason,omitempty"`
	BaseAmountPaise  int64  `json:"base_amount_paise"`
	FinalAmountPaise int64  `json:"final_amount_paise"`
	PricePaise       int64  `json:"price_paise"`
	WalletPaise      int64  `json:"wallet_paise"`
	SpendLimitPaise  int64  `json:"spend_limit_paise"`
	TotalPaise       int64  `json:"total_paise"`
}

// Decision is a model or deterministic intent. It cannot mutate payment state.
type Decision struct {
	Action        DecisionAction `json:"action"`
	ProductID     string         `json:"product_id"`
	Quantity      int            `json:"quantity"`
	MaxSpendPaise int64          `json:"max_spend_paise"`
	Rationale     string         `json:"rationale"`
}

// Config controls the optional OpenAI-compatible reasoning model.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// FromEnv loads the model configuration without selecting a provider-specific default.
func FromEnv() Config {
	return Config{APIKey: strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), BaseURL: strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), Model: strings.TrimSpace(os.Getenv("ADK_MODEL_NAME"))}
}

// Service decides bounded intent and falls back to deterministic policy when disabled.
type Service struct {
	runner   *runner.Runner
	sessions atomic.Uint64
}

// New builds a reasoning service. Empty configuration deliberately selects fallback policy.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.APIKey == "" || cfg.Model == "" {
		return &Service{}, nil
	}
	model, err := openaimodel.NewModel(ctx, cfg.Model, &openaimodel.ClientConfig{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	if err != nil {
		return nil, fmt.Errorf("configure reasoning model: %w", err)
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        "purchase_reasoner",
		Description: "Select a bounded purchase intent from trusted facts.",
		Model:       model,
		Instruction: "Return only JSON matching {\"action\":\"buy|negotiate|ask_human|decline\",\"product_id\":string,\"quantity\":integer,\"max_spend_paise\":integer,\"rationale\":string}. Use trusted catalog facts, stock, warranty, trust score, offer reason, and budget in the rationale. Never claim that money moved. Use ask_human when the request exceeds the spend limit or facts are missing.",
	})
	if err != nil {
		return nil, fmt.Errorf("create reasoning agent: %w", err)
	}
	r, err := runner.NewInMemory("agentmart-reasoning", a)
	if err != nil {
		return nil, fmt.Errorf("create reasoning runner: %w", err)
	}
	return &Service{runner: r}, nil
}

// Decide returns a validated intent and never performs a wallet or payment operation.
func (s *Service) Decide(ctx context.Context, input Input) (Decision, error) {
	if s.runner == nil {
		return deterministic(input), nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Decision{}, fmt.Errorf("encode reasoning input: %w", err)
	}
	content := genai.NewContentFromText(string(payload), genai.RoleUser)
	var output string
	sessionID := fmt.Sprintf("decision-%d", s.sessions.Add(1))
	for event, runErr := range s.runner.Run(ctx, "buyer", sessionID, content, agent.RunConfig{}) {
		if runErr != nil {
			return Decision{}, fmt.Errorf("run reasoning decision: %w", runErr)
		}
		if event != nil && event.Content != nil {
			for _, part := range event.Content.Parts {
				if part != nil && part.Text != "" {
					output = part.Text
				}
			}
		}
	}
	var decision Decision
	if err := json.Unmarshal([]byte(output), &decision); err != nil {
		return Decision{}, fmt.Errorf("decode reasoning decision: %w", err)
	}
	return validate(input, decision)
}

func deterministic(input Input) Decision {
	decision := Decision{ProductID: input.ProductID, Quantity: input.Quantity, MaxSpendPaise: input.SpendLimitPaise, Rationale: "deterministic policy"}
	if input.ProductID == "" || input.Quantity <= 0 || input.PricePaise <= 0 {
		decision.Action = ActionAskHuman
		return decision
	}
	total := input.TotalPaise
	if total <= 0 {
		total = input.PricePaise * int64(input.Quantity)
	}
	if input.SpendLimitPaise > 0 && total > input.SpendLimitPaise {
		decision.Action = ActionAskHuman
		return decision
	}
	decision.Action = ActionBuy
	return decision
}

func validate(input Input, decision Decision) (Decision, error) {
	if decision.ProductID == "" {
		decision.ProductID = input.ProductID
	}
	if decision.Quantity <= 0 {
		decision.Quantity = input.Quantity
	}
	if decision.ProductID != input.ProductID || decision.Quantity != input.Quantity {
		return Decision{}, errors.New("reasoning decision cannot change trusted product or quantity")
	}
	if strings.TrimSpace(decision.Rationale) == "" {
		return Decision{}, errors.New("reasoning decision requires rationale")
	}
	switch decision.Action {
	case ActionBuy, ActionNegotiate, ActionAskHuman, ActionDecline:
		return decision, nil
	default:
		return Decision{}, fmt.Errorf("unsupported reasoning action %q", decision.Action)
	}
}
