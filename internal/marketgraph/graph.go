// Package marketgraph is the merchant side of AgentMart as a workflow
// graph: deterministic campaign/eligibility facts feed an LLM pricing
// strategist, whose amount is then clamped by a price guard that can never
// sell below the merchant's cost floor. Every decision carries an explanation
// so the audit trail reads as prose, not magic numbers.
//
//	START  to  campaign(fn)  to  strategy(LLM)  to  guard(fn)  to  END
//
// The graph implements negotiation.Negotiator, so the negotiation server
// gets an agentic orchestrator without changing its wiring.
package marketgraph

import (
	"context"
	"fmt"
	"time"

	"agentmart/internal/negotiation"
)

// Strategy names the merchant's chosen play. The LLM picks one; the guard
// enforces that the money matches it.
type Strategy string

const (
	// StrategyHold keeps the standing ask: the buyer's counter is too thin.
	StrategyHold Strategy = "hold"
	// StrategyConcede moves toward the buyer inside the concession schedule.
	StrategyConcede Strategy = "concede"
	// StrategyBundle keeps price but sweetens with the combo partner.
	StrategyBundle Strategy = "bundle_sweetener"
	// StrategyLoyalty applies a campaign/loyalty discount to a returning buyer.
	StrategyLoyalty Strategy = "loyalty_discount"
)

// Facts are everything the merchant strategist may reason over. They are
// assembled deterministically; the model never sources its own data.
type Facts struct {
	// Money rails.
	AskPaise           int64 `json:"ask_paise"`
	FloorPaise         int64 `json:"floor_paise"`
	BuyerPaise         int64 `json:"buyer_paise"`
	MinAcceptablePaise int64 `json:"min_acceptable_paise"`

	// Conversation position.
	Round     int `json:"round"`
	MaxRounds int `json:"max_rounds"`

	// Product signals.
	ProductName   string `json:"product_name,omitempty"`
	Category      string `json:"category,omitempty"`
	WarrantyYears int    `json:"warranty_years"`
	TrustScore    int    `json:"trust_score"`
	Stock         int    `json:"stock"`
	BundleName    string `json:"bundle_name,omitempty"`

	// Campaign layer (filled by the campaign node).
	LoyaltyTier        string   `json:"loyalty_tier,omitempty"`
	LoyaltyDiscountPct int      `json:"loyalty_discount_pct"`
	CampaignNotes      []string `json:"campaign_notes,omitempty"`

	Transcript []string `json:"transcript,omitempty"`
}

// StrategyChoice is the strategist node's output contract.
type StrategyChoice struct {
	Facts       Facts    `json:"facts"`
	Strategy    Strategy `json:"strategy"`
	AmountPaise int64    `json:"amount_paise"`
	Reason      string   `json:"reason"`
}

// Decision is the guarded, explainable merchant answer.
type Decision struct {
	AmountPaise int64    `json:"amount_paise"`
	Reason      string   `json:"reason"`
	Strategy    Strategy `json:"strategy"`
	GuardNote   string   `json:"guard_note,omitempty"`
	MarginPaise int64    `json:"margin_paise"`
}

// CampaignProvider lets the market binary supply account-specific deals. It is
// optional: without one the graph derives tiers from merchant-side signals only.
type CampaignProvider interface {
	// Eligibility returns a loyalty tier label, a discount percentage the
	// merchant is willing to fund, and human-readable notes for the audit.
	Eligibility(ctx context.Context, in negotiation.CounterInput) (tier string, discountPct int, notes []string, err error)
}

// Auditor persists the merchant agent's explanation for every priced offer.
// The guard node fails closed when auditing fails, mirroring the Gate: an
// unexplainable price is not allowed to reach the buyer.
type Auditor interface {
	RecordOfferDecision(ctx context.Context, in negotiation.CounterInput, facts Facts, decision Decision) error
}

// Per-node bounds keep a slow provider from stalling a negotiation round.
const (
	strategyTimeout = 60 * time.Second
	graphTimeout    = 120 * time.Second
	maxGraphEvents  = 60
)

// clampToRails keeps any proposed amount inside [minFloor, ask] and reports
// what it changed. This is the bounded-money guarantee: the LLM cannot sell at
// a loss, cannot exceed its own ask, and cannot undercut the buyer's own bid.
// clampToRails is the money boundary. The strategist proposes; this decides. The
// low bound is whichever of the cost floor, the buyer's own bid and the round's
// concession floor is highest, and nothing may exceed the standing ask. Naming the
// bound that bit is what makes the correction explainable in the trail.
func clampToRails(proposed, floor, buyerPaise, minAcceptable, ask int64) (int64, string) {
	low, bound := floor, "the cost floor"
	if buyerPaise > low {
		low, bound = buyerPaise, "the buyer's own bid"
	}
	if minAcceptable > low {
		low, bound = minAcceptable, "this round's concession floor"
	}
	// A low bound above the standing ask would let the merchant charge more than
	// it asked for, so the ask wins over every floor.
	if ask > 0 && low > ask {
		low, bound = ask, "the standing ask"
	}
	switch {
	case ask <= 0:
		return low, "no standing ask; held at " + bound
	case proposed < low:
		return low, fmt.Sprintf("raised to %d: strategy amount was below %s", low, bound)
	case proposed > ask:
		return ask, fmt.Sprintf("lowered to %d: strategy amount exceeded the standing ask", ask)
	default:
		return proposed, ""
	}
}
