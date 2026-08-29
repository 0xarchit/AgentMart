// Package shopgraph builds AgentMart's buyer agent as a typed workflow
// graph: LLM decision nodes (intent / selection / negotiation) interleaved
// with deterministic function nodes (catalog lookup, offer fetch,
// final verification). The graph is wrapped by
// a workflow agent so it is itself a standard agent.
package shopgraph

import (
	"context"
	"fmt"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
)

// Action is the settled outcome kind.
type Action string

const (
	ActionBuy      Action = "buy"
	ActionAskHuman Action = "ask_human"
	ActionDecline  Action = "declined"
)

// Wallet carries the trusted money ceilings for one run.
type Wallet struct {
	BalancePaise    int64
	SpendLimitPaise int64
	BudgetPaise     int64  // user-stated budget; 0 = use spend limit
	AccountID       string // identifies the buyer to the merchant for campaign deals
}

// Result is what the caller uses after the graph finishes.
type Result struct {
	Action        Action
	ProductID     string
	ProductName   string
	Quantity      int
	FinalPaise    int64
	Rationale     string
	Steps         []string
	SessionID     string
	Transcript    []negotiation.Turn
	Accepted      bool
	NeedsApproval bool // informational: premium band crossed on an in-budget offer
}

// Assessment is the buyer agent's judgement on a merchant offer. The agent,
// not a threshold in code, chooses what happens next.
type Assessment struct {
	Decision string `json:"decision"` // accept | negotiate | ask_human | decline
	Reason   string `json:"reason"`
}

// OfferView is what the assessing agent sees: the merchant's offer plus the
// user's money facts, so its judgement is grounded in both instead of in a
// hardcoded rule.
type OfferView struct {
	Offer
	WalletBalancePaise int64 `json:"wallet_balance_paise"`
	SpendLimitPaise    int64 `json:"spend_limit_paise"`
	BudgetPaise        int64 `json:"budget_paise"`
	PremiumPaise       int64 `json:"premium_over_list_paise"`
	PremiumPct         int   `json:"premium_over_list_pct"`
	AdvisoryBandPct    int   `json:"advisory_band_pct"`
}

// Tools are the graph's hands: the shop conversation plus the one catalog read
// used to re-verify what was bought. Money movement stays outside: purchases
// execute through PurchaseService after Run.
type Tools struct {
	Browse  func(ctx context.Context, brief string, budgetPaise int64, accountID string) (negotiationclient.Shortlist, error)
	Get     func(ctx context.Context, id string) (catalog.Product, error)
	Offers  func(ctx context.Context, id string, qty int, accountID string) (negotiationclient.Proposal, error)
	Counter func(ctx context.Context, sessionID string, paise int64) (negotiationclient.Resolution, error)
	Accept  func(ctx context.Context, sessionID string) (negotiationclient.Resolution, error)
	Decline func(ctx context.Context, sessionID string, reason string) (negotiationclient.Resolution, error)
}

func (t Tools) validate() error {
	if t.Browse == nil || t.Get == nil || t.Offers == nil || t.Counter == nil || t.Accept == nil || t.Decline == nil {
		return fmt.Errorf("shopgraph tools are incomplete")
	}
	return nil
}

// Pick is the buyer's choice from the shop's shortlist, carrying the opening
// conversation forward so the final transcript starts where the talking did.
type Pick struct {
	Selection
	ShopTranscript []negotiation.Turn `json:"-"`
}

// Selection is the product chosen by the selection node.
type Selection struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
	Rationale string `json:"rationale"`
}

// Offer is the merchant's opening quote for the selected product.
type Offer struct {
	SessionID   string `json:"session_id"`
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	Quantity    int    `json:"quantity"`
	BasePaise   int64  `json:"base_amount_paise"`
	FinalPaise  int64  `json:"final_amount_paise"`
	// BundledPaise is what the ask charges for goods attached to the main
	// product. It is list value the buyer receives, not markup on the product.
	BundledPaise int64              `json:"bundled_paise,omitempty"`
	Reason       string             `json:"reason"`
	Route        string             `json:"-"`
	Transcript   []negotiation.Turn `json:"transcript,omitempty"`
	// ShopTurns are the opening turns from browsing. They are ours, not the
	// merchant's session, so they must be carried separately or a settlement
	// that returns the session transcript would erase them.
	ShopTurns []negotiation.Turn `json:"-"`
}

// Outcome is the settled negotiation state flowing into finalize.
type Outcome struct {
	Action      string `json:"action"`
	Status      string `json:"status"` // accepted | countered | declined | needs_human
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	Quantity    int    `json:"quantity"`
	FinalPaise  int64  `json:"final_amount_paise"`
	// BundledPaise carries the attached goods forward so the settled amount is
	// still measured against everything the buyer receives.
	BundledPaise int64              `json:"bundled_paise,omitempty"`
	Rationale    string             `json:"rationale"`
	Steps        []string           `json:"steps,omitempty"`
	SessionID    string             `json:"session_id"`
	Accepted     bool               `json:"accepted"`
	Transcript   []negotiation.Turn `json:"transcript,omitempty"`
}
