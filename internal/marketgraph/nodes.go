// Node construction, graph wiring, and the negotiation.Negotiator adapter.
package marketgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"agentmart/internal/llmchat"
	"agentmart/internal/negotiation"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Config selects the provider backing the strategist node.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// Negotiator runs the merchant graph. It satisfies negotiation.Negotiator.
type Negotiator struct {
	graph     agent.Agent
	campaigns CampaignProvider
	auditor   Auditor
	sessions  atomic.Uint64
	pending   atomic.Pointer[negotiation.CounterInput]
}

// New builds the merchant graph. Returns (nil, nil) when no model is
// configured so the caller keeps its deterministic concession schedule.
// The auditor is optional; when supplied, an offer that cannot be explained in
// the audit trail is not returned to the buyer.
func New(cfg Config, campaigns CampaignProvider, auditor Auditor) (*Negotiator, error) {
	if cfg.APIKey == "" || cfg.Model == "" {
		return nil, nil
	}
	n := &Negotiator{campaigns: campaigns, auditor: auditor}
	graph, err := n.buildGraph(cfg)
	if err != nil {
		return nil, err
	}
	n.graph = graph
	return n, nil
}

const strategyInstruction = `You are the merchant's pricing strategist in a live
agent-to-agent negotiation. You receive Facts: the standing ask, the cost floor
(your absolute minimum), the buyer's counter, the concession schedule's
min_acceptable_paise for this round, product signals (warranty, trust score,
stock), any bundle partner, and campaign eligibility for this buyer.

Choose exactly one strategy and one amount:
- "hold": keep ask_paise. Use when the buyer's counter is far below
  min_acceptable_paise and rounds remain.
- "concede": move toward the buyer but never below min_acceptable_paise.
- "bundle_sweetener": keep the price near the ask and justify it with the
  bundle partner's value.
- "loyalty_discount": only when loyalty_discount_pct > 0; price down by at most
  that percentage of the ask, never below floor_paise.

Rules you cannot break: amount_paise must be >= floor_paise, >= buyer_paise,
and <= ask_paise. Selling below the floor is a loss and is forbidden.

Return only JSON: {"strategy":"...","amount_paise":integer,"reason":"one short
sentence a customer would accept"}.`

// buildGraph wires campaign → strategy → guard and wraps it as an agent.
func (n *Negotiator) buildGraph(cfg Config) (agent.Agent, error) {
	strategist, err := llmagent.New(llmagent.Config{
		Name:        "merchant-strategist",
		Description: "Chooses one pricing strategy and amount inside the owner's rails.",
		Model:       llmchat.New(cfg.Model, cfg.APIKey, cfg.BaseURL),
		Instruction: strategyInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("merchant strategist agent: %w", err)
	}

	// campaignNode turns the raw negotiation input into Facts, adding the
	// campaign/loyalty layer. Pure data assembly: no judgement here.
	campaignNode := workflow.NewFunctionNode[string, Facts]("campaign_eligibility",
		func(ctx agent.Context, _ string) (Facts, error) {
			input := n.pending.Load()
			if input == nil {
				return Facts{}, fmt.Errorf("no negotiation input in flight")
			}
			facts := factsFrom(*input)
			tier, pct, notes := n.eligibility(ctx, *input)
			facts.LoyaltyTier, facts.LoyaltyDiscountPct = tier, pct
			facts.CampaignNotes = notes
			return facts, nil
		}, workflow.NodeConfig{})

	strategyNode, err := workflow.NewAgentNodeTyped[Facts, StrategyChoice](strategist,
		workflow.NodeConfig{Timeout: strategyTimeout})
	if err != nil {
		return nil, fmt.Errorf("merchant strategy node: %w", err)
	}

	// guardNode is the bounded-money boundary: it clamps whatever the model
	// asked for into the rails and explains any correction.
	guardNode := workflow.NewFunctionNode[StrategyChoice, Decision]("price_guard",
		func(ctx agent.Context, choice StrategyChoice) (Decision, error) {
			input := n.pending.Load()
			if input == nil {
				return Decision{}, fmt.Errorf("no negotiation input in flight")
			}
			facts := factsFrom(*input)
			amount, note := clampToRails(choice.AmountPaise, facts.FloorPaise, facts.BuyerPaise, facts.AskPaise)
			strategy := choice.Strategy
			if strategy == "" {
				strategy = StrategyConcede
			}
			reason := strings.TrimSpace(choice.Reason)
			if reason == "" {
				reason = "best price we can hold while keeping the quality guarantees"
			}
			decision := Decision{
				AmountPaise: amount,
				Reason:      reason,
				Strategy:    strategy,
				GuardNote:   note,
				MarginPaise: amount - facts.FloorPaise,
			}
			// Fail closed on auditing, exactly like the Gate: a price the
			// merchant cannot explain in the trail never reaches the buyer.
			if n.auditor != nil {
				if err := n.auditor.RecordOfferDecision(ctx, *input, facts, decision); err != nil {
					return Decision{}, fmt.Errorf("audit merchant offer: %w", err)
				}
			}
			return decision, nil
		}, workflow.NodeConfig{})

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, campaignNode).
		Add(campaignNode, strategyNode).
		Add(strategyNode, guardNode).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "merchant-agent",
		Description: "Prices and negotiates merchant offers without selling below cost.",
		Edges:       edges,
	})
}

// eligibility resolves the campaign layer, falling back to merchant-side
// signals when no provider is wired.
func (n *Negotiator) eligibility(ctx context.Context, input negotiation.CounterInput) (string, int, []string) {
	if n.campaigns != nil {
		tier, pct, notes, err := n.campaigns.Eligibility(ctx, input)
		if err == nil {
			return tier, pct, notes
		}
		return "standard", 0, []string{fmt.Sprintf("campaign lookup failed: %v", err)}
	}
	// Merchant-side signals only: slow-moving stock earns a small funded
	// discount, scarce stock earns none.
	switch {
	case input.Product.Stock >= 25:
		return "stock_clearance", 5, []string{"high stock on hand: 5% clearance budget available"}
	case input.Product.Stock <= 3:
		return "scarce", 0, []string{"low stock: no discount budget"}
	default:
		return "standard", 0, nil
	}
}

// factsFrom assembles the deterministic view the strategist reasons over.
func factsFrom(input negotiation.CounterInput) Facts {
	facts := Facts{
		AskPaise: input.AskPaise, FloorPaise: input.FloorPaise,
		BuyerPaise: input.BuyerPaise, MinAcceptablePaise: input.MinAcceptablePaise,
		Round: input.Session.Round, MaxRounds: negotiation.MaxRounds,
		ProductName: input.Product.Name, Category: input.Product.Category,
		WarrantyYears: input.Product.WarrantyYears, TrustScore: input.Product.TrustScore,
		Stock: input.Product.Stock,
	}
	if input.Partner != nil {
		facts.BundleName = input.Partner.Name
	}
	for _, turn := range input.Session.Transcript {
		facts.Transcript = append(facts.Transcript, turn.Actor+": "+turn.Message)
	}
	return facts
}

// Counter implements negotiation.Negotiator by running one graph pass.
func (n *Negotiator) Counter(ctx context.Context, input negotiation.CounterInput) (negotiation.CounterOutput, error) {
	decision, err := n.Decide(ctx, input)
	if err != nil {
		return negotiation.CounterOutput{}, err
	}
	reason := decision.Reason
	if decision.GuardNote != "" {
		reason = fmt.Sprintf("%s (%s)", reason, decision.GuardNote)
	}
	return negotiation.CounterOutput{AmountPaise: decision.AmountPaise, Reason: reason}, nil
}

// Decide runs the graph and returns the full explainable decision.
func (n *Negotiator) Decide(parent context.Context, input negotiation.CounterInput) (Decision, error) {
	if n == nil || n.graph == nil {
		return Decision{}, fmt.Errorf("merchant graph is not configured")
	}
	n.pending.Store(&input)
	defer n.pending.Store(nil)

	run, err := runner.NewInMemory("agentmart-merchant", n.graph)
	if err != nil {
		return Decision{}, fmt.Errorf("merchant runner: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, graphTimeout)
	defer cancel()

	sessionID := fmt.Sprintf("merchant-%d", n.sessions.Add(1))
	var last any
	events := 0
	for event, runErr := range run.Run(ctx, "merchant", sessionID,
		genai.NewContentFromText("negotiate", genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			return Decision{}, fmt.Errorf("merchant graph failed after %d events: %w", events, runErr)
		}
		events++
		if events > maxGraphEvents {
			return Decision{}, fmt.Errorf("merchant graph exceeded %d events", maxGraphEvents)
		}
		if event != nil && event.Output != nil {
			last = event.Output
		}
	}
	decision, ok := decisionFromAny(last)
	if !ok {
		return Decision{}, fmt.Errorf("merchant graph produced %T instead of a Decision", last)
	}
	return decision, nil
}

// decisionFromAny accepts the typed value or its JSON projection.
func decisionFromAny(raw any) (Decision, bool) {
	switch v := raw.(type) {
	case Decision:
		return v, true
	case map[string]any, string:
		encoded, err := json.Marshal(v)
		if err != nil {
			return Decision{}, false
		}
		if s, isString := v.(string); isString {
			encoded = []byte(s)
		}
		var decision Decision
		if json.Unmarshal(encoded, &decision) == nil && decision.AmountPaise > 0 {
			return decision, true
		}
	}
	return Decision{}, false
}
