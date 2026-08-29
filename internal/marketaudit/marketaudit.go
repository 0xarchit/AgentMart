// Package marketaudit persists the merchant agent's pricing explanations to the
// shared audit trail, so every offer the buyer sees can be justified after the
// fact: which strategy was chosen, what the rails were, and what the guard
// corrected.
package marketaudit

import (
	"context"
	"fmt"

	"agentmart/internal/marketgraph"
	"agentmart/internal/negotiation"
	"agentmart/internal/runid"
	"agentmart/internal/supabase"
)

// Store writes merchant agent decisions through the trusted service client.
type Store struct {
	db *supabase.Client
}

// New constructs a merchant auditor. A nil db disables auditing, which also
// disables the fail-closed guarantee, so prefer wiring a real client.
func New(db *supabase.Client) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// RecordOfferDecision implements marketgraph.Auditor.
func (s *Store) RecordOfferDecision(ctx context.Context, in negotiation.CounterInput, facts marketgraph.Facts, decision marketgraph.Decision) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("merchant auditor is not configured")
	}
	reason := decision.Reason
	if decision.GuardNote != "" {
		reason = fmt.Sprintf("%s | guard: %s", reason, decision.GuardNote)
	}
	row := map[string]any{
		"actor":  "merchant_agent",
		"action": "offer_priced",
		"reason": reason,
		"payload": map[string]any{
			"strategy":             decision.Strategy,
			"amount_paise":         decision.AmountPaise,
			"margin_paise":         decision.MarginPaise,
			"guard_note":           decision.GuardNote,
			"ask_paise":            facts.AskPaise,
			"floor_paise":          facts.FloorPaise,
			"buyer_paise":          facts.BuyerPaise,
			"min_acceptable_paise": facts.MinAcceptablePaise,
			"round":                facts.Round,
			"max_rounds":           facts.MaxRounds,
			"product_id":           in.Product.ID,
			"product_name":         facts.ProductName,
			"bundle_name":          facts.BundleName,
			"loyalty_tier":         facts.LoyaltyTier,
			"loyalty_discount_pct": facts.LoyaltyDiscountPct,
			"campaign_notes":       facts.CampaignNotes,
		},
	}
	// audit_log.account_id is nullable: an anonymous agent caller still gets a
	// merchant-side trail, just without buyer attribution.
	if in.BuyerAccountID != "" {
		row["account_id"] = in.BuyerAccountID
	}
	// The buyer named the run on the wire, so the shop's pricing explanation
	// joins the same story rather than sitting in the trail unattached.
	if id := runid.From(ctx); id != "" {
		row["run_id"] = id
	}
	return s.db.Insert(ctx, "audit_log", row, nil)
}
