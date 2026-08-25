// Package agentloop runs the bounded buyer agent: an LLM tool loop with a
// deterministic fallback, a hard premium-band HITL rule, and full tracing.
// Money movement stays outside this package — purchases execute through
// PurchaseService after Run settles.
package agentloop

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	buyerreasoning "agentmart/internal/reasoning"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
)

// AutoBuyPremiumMaxPct is the largest premium over the main product's list
// price the agent may accept without asking the human.
const AutoBuyPremiumMaxPct = 30

// Run budgets. Breaching any of them falls back to deterministic policy.
const (
	maxToolCalls = 6
	runDeadline  = 45 * time.Second
)

// Action is what the supervisor should do after the loop settles.
type Action string

const (
	ActionBuy      Action = "buy"
	ActionAskHuman Action = "ask_human"
	ActionDecline  Action = "decline"
)

// Tools are the buyer agent's senses plus its negotiation voice. Money
// movement is NOT here — purchases go through PurchaseService after Run.
type Tools struct {
	Search  func(ctx context.Context, query string, maxPaise int64) ([]catalog.Product, error)
	Get     func(ctx context.Context, id string) (catalog.Product, error)
	Offers  func(ctx context.Context, id string, qty int) (negotiationclient.Proposal, error)
	Counter func(ctx context.Context, sessionID string, paise int64) (negotiationclient.Resolution, error)
}

func (t Tools) validate() error {
	if t.Search == nil || t.Get == nil || t.Offers == nil || t.Counter == nil {
		return fmt.Errorf("agentloop tools are incomplete")
	}
	return nil
}

// WalletFacts carries the trusted money ceilings for one run.
type WalletFacts struct {
	BalancePaise    int64
	SpendLimitPaise int64
	BudgetPaise     int64 // user-stated budget; 0 means "use spend limit"
}

// LoopResult is the settled outcome of one run.
type LoopResult struct {
	Action        Action
	Product       catalog.Product
	Quantity      int
	FinalPaise    int64
	Rationale     string
	Steps         []string
	SessionID     string
	Transcript    []negotiation.Turn
	NeedsApproval bool // true when ask_human was about an in-budget premium offer
}

// Service runs bounded loops. Without model configuration every run takes the
// deterministic path, so demos never depend on network luck.
type Service struct {
	runner   *runner.Runner
	sessions atomic.Uint64
	tools    Tools
}

// New builds the service. Empty model configuration selects deterministic mode.
func New(ctx context.Context, cfg buyerreasoning.Config, tools Tools) (*Service, error) {
	if err := tools.validate(); err != nil {
		return nil, err
	}
	svc := &Service{tools: tools}
	if cfg.APIKey == "" || cfg.Model == "" {
		return svc, nil
	}
	// Chat-completions adapter instead of ADK openaimodel: openaimodel speaks
	// only OpenAI's Responses API, which OpenRouter/NVIDIA/OpenCode-style
	// gateways reject. /chat/completions works everywhere.
	model := NewChatModel(cfg.Model, cfg.APIKey, cfg.BaseURL)
	a, err := llmagent.New(llmagent.Config{
		Name:        "buyer-agent",
		Description: "Resolve a natural-language purchase request against the merchant catalog and negotiate inside bounds.",
		Model:       model,
		Instruction: instruction,
		Tools:       svc.modelTools(),
	})
	if err != nil {
		return nil, fmt.Errorf("create buyer agent: %w", err)
	}
	r, err := runner.NewInMemory("agentmart-buyer", a)
	if err != nil {
		return nil, fmt.Errorf("create buyer agent runner: %w", err)
	}
	svc.runner = r
	return svc, nil
}

const instruction = `You are AgentMart's buying agent acting for one user. Resolve their request using the tools: search_catalog to find matching products, get_product for details, get_offers for the merchant quote including any combo bundle, and counter_offer at most once when the quote clearly exceeds fair value. Facts you receive include wallet_balance_paise, spend_limit_paise, budget_paise, and premium_band_pct. Rules you cannot change: prefer totals within budget_paise (or spend_limit_paise when no budget was stated); never exceed wallet_balance_paise; when the best total exceeds the premium band above the main product list price, call request_human instead of deciding; otherwise call finish with action "buy" or "decline", the chosen session_id, product_id, quantity, and final_paise exactly as returned by get_offers or counter_offer. Never invent prices, products, or payments.`

// Run resolves one natural-language request into a validated outcome.
func (s *Service) Run(parent context.Context, request string, wallet WalletFacts) LoopResult {
	ctx, cancel := context.WithTimeout(parent, runDeadline)
	defer cancel()
	state := newState(wallet, strings.TrimSpace(request))

	if s.runner == nil {
		fallbackRun(ctx, s.tools, state)
	} else if err := s.llmRun(ctx, state); err != nil {
		state.step(fmt.Sprintf("LLM loop failed (%v); using deterministic policy", err))
		fallbackRun(ctx, s.tools, state)
	}
	return s.settle(ctx, state)
}

// settle re-verifies the candidate against authoritative facts and applies the
// premium-band rule. The model's opinion never survives this function unchecked.
func (s *Service) settle(ctx context.Context, state *runState) LoopResult {
	result := LoopResult{Action: ActionDecline, Rationale: state.rationale, Steps: state.steps,
		Quantity: state.quantity, FinalPaise: state.finalPaise, SessionID: state.sessionID,
		Transcript: state.transcript}

	if strings.TrimSpace(state.productID) == "" {
		result.Rationale = joinReason(state.rationale, "no matching product found")
		return result
	}
	product, err := s.tools.Get(ctx, state.productID)
	if err != nil {
		result.Rationale = joinReason(state.rationale, fmt.Sprintf("candidate vanished from catalog: %v", err))
		return result
	}
	result.Product = product
	if result.Quantity <= 0 {
		result.Quantity = 1
	}

	baseMain := product.PricePaise * int64(result.Quantity)
	final := result.FinalPaise
	if final <= 0 {
		final = baseMain
	}
	result.FinalPaise = final

	premium := final - baseMain
	ceiling := state.wallet.BudgetPaise
	if ceiling <= 0 {
		ceiling = state.wallet.SpendLimitPaise
	}

	switch {
	case final > state.wallet.BalancePaise:
		result.Rationale = joinReason(state.rationale,
			fmt.Sprintf("total INR %.2f exceeds wallet balance INR %.2f", float64(final)/100, float64(state.wallet.BalancePaise)/100))
	case ceiling > 0 && final > ceiling:
		result.Rationale = joinReason(state.rationale,
			fmt.Sprintf("total INR %.2f exceeds budget INR %.2f", float64(final)/100, float64(ceiling)/100))
	case baseMain > 0 && premium*100 > baseMain*AutoBuyPremiumMaxPct:
		result.Action = ActionAskHuman
		result.NeedsApproval = true
		result.Rationale = joinReason(state.rationale,
			fmt.Sprintf("premium INR %.2f crosses the %d%% auto-buy band; asking the human", float64(premium)/100, AutoBuyPremiumMaxPct))
	default:
		result.Action = ActionBuy
		result.Rationale = joinReason(state.rationale,
			fmt.Sprintf("within budget and the %d%% premium band", AutoBuyPremiumMaxPct))
	}

	state.step(fmt.Sprintf("decision=%s total=%d base=%d", result.Action, final, baseMain))
	result.Steps = state.steps
	return result
}

func joinReason(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, "; ")
}
