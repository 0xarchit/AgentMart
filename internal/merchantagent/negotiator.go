// The merchant agent chooses counter amounts and wording inside orchestrator rails.
package merchantagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"agentmart/internal/negotiation"
	buyerreasoning "agentmart/internal/reasoning"

	"agentmart/internal/llmchat"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// Negotiator is the LLM-backed implementation of negotiation.Negotiator.
type Negotiator struct {
	runner   *runner.Runner
	sessions atomic.Uint64
}

// NewNegotiator returns a nil-safe negotiator: without model configuration the
// caller keeps the deterministic concession schedule.
func NewNegotiator(ctx context.Context, cfg buyerreasoning.Config) (*Negotiator, error) {
	if cfg.APIKey == "" || cfg.Model == "" {
		return nil, nil
	}
	model := llmchat.New(cfg.Model, cfg.APIKey, cfg.BaseURL)
	a, err := llmagent.New(llmagent.Config{
		Name:        "merchant-negotiator",
		Description: "Choose one counter amount inside the owner's rails.",
		Model:       model,
		Instruction: "You negotiate for the merchant. Given the standing ask, cost floor, buyer counter, and conversation so far, choose ONE counter amount strictly between max(floor_paise, buyer_paise) and ask_paise, plus one short persuasive sentence referencing warranty, trust, stock, or bundle value. Never go below floor_paise; that would sell at a loss. Return only JSON {\"amount_paise\":integer,\"reason\":string}.",
	})
	if err != nil {
		return nil, fmt.Errorf("create merchant negotiator agent: %w", err)
	}
	r, err := runner.NewInMemory("agentmart-merchant", a)
	if err != nil {
		return nil, fmt.Errorf("create merchant negotiator runner: %w", err)
	}
	return &Negotiator{runner: r}, nil
}

type negotiatorFacts struct {
	AskPaise       int64    `json:"ask_paise"`
	FloorPaise     int64    `json:"floor_paise"`
	BuyerPaise     int64    `json:"buyer_paise"`
	Round          int      `json:"round"`
	MaxRounds      int      `json:"max_rounds"`
	ProductName    string   `json:"product_name,omitempty"`
	Bundle         []string `json:"bundle,omitempty"`
	SuggestedPaise int64    `json:"suggested_paise"` // deterministic schedule anchor
	Transcript     []string `json:"transcript,omitempty"`
}

type negotiatorOutput struct {
	AmountPaise int64  `json:"amount_paise"`
	Reason      string `json:"reason"`
}

// Counter asks the model for one counter inside the rails; callers clamp again.
func (n *Negotiator) Counter(ctx context.Context, input negotiation.CounterInput) (negotiation.CounterOutput, error) {
	if n == nil || n.runner == nil {
		return negotiation.CounterOutput{}, fmt.Errorf("merchant negotiator is not configured")
	}
	facts := negotiatorFacts{
		AskPaise: input.AskPaise, FloorPaise: input.FloorPaise, BuyerPaise: input.BuyerPaise,
		Round: input.Session.Round, MaxRounds: negotiation.MaxRounds,
		ProductName: input.Product.Name, SuggestedPaise: input.MinAcceptablePaise,
	}
	if input.Partner != nil {
		facts.Bundle = append(facts.Bundle, input.Partner.Name)
	}
	for _, turn := range input.Session.Transcript {
		facts.Transcript = append(facts.Transcript, turn.Actor+": "+turn.Message)
	}
	payload, err := json.Marshal(facts)
	if err != nil {
		return negotiation.CounterOutput{}, fmt.Errorf("encode negotiator facts: %w", err)
	}
	var output string
	sessionID := fmt.Sprintf("merchant-counter-%d", n.sessions.Add(1))
	for event, runErr := range n.runner.Run(ctx, "merchant", sessionID, genai.NewContentFromText(string(payload), genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			break
		}
		if event != nil && event.Content != nil {
			for _, part := range event.Content.Parts {
				if part != nil && part.Text != "" {
					output = strings.TrimSpace(part.Text)
				}
			}
		}
	}
	if output == "" {
		return negotiation.CounterOutput{}, fmt.Errorf("merchant negotiator returned no content")
	}
	output = strings.TrimPrefix(output, "```json")
	output = strings.TrimSuffix(strings.TrimSpace(output), "```")
	var parsed negotiatorOutput
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return negotiation.CounterOutput{}, fmt.Errorf("decode merchant negotiator output: %w", err)
	}
	if strings.TrimSpace(parsed.Reason) == "" {
		parsed.Reason = "best we can do while keeping quality guarantees"
	}
	return negotiation.CounterOutput{AmountPaise: parsed.AmountPaise, Reason: parsed.Reason}, nil
}
