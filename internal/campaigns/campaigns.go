// Package campaigns resolves merchant-funded, per-account deals from real order
// history. It is the CampaignProvider the merchant graph consults before its
// strategist prices an offer, so personalisation comes from facts.
package campaigns

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/negotiation"
	"agentmart/internal/supabase"
)

// Provider reads campaign eligibility through the trusted RPC.
type Provider struct {
	db *supabase.Client
}

// NewProvider constructs a campaign provider. A nil db disables campaigns.
func NewProvider(db *supabase.Client) *Provider {
	if db == nil {
		return nil
	}
	return &Provider{db: db}
}

type eligibility struct {
	Tier        string   `json:"tier"`
	DiscountPct int      `json:"discount_pct"`
	Campaign    string   `json:"campaign,omitempty"`
	Orders      int      `json:"orders"`
	SpendPaise  int64    `json:"spend_paise"`
	Notes       []string `json:"notes,omitempty"`
}

// Eligibility implements marketgraph.CampaignProvider.
func (p *Provider) Eligibility(ctx context.Context, in negotiation.CounterInput) (string, int, []string, error) {
	if p == nil || p.db == nil {
		return "standard", 0, nil, fmt.Errorf("campaign provider is not configured")
	}
	account := strings.TrimSpace(in.BuyerAccountID)
	if account == "" {
		// Anonymous A2A caller: no history, so no funded discount. Still a
		// valid negotiation, just without personalisation.
		return "standard", 0, []string{"no buyer account supplied: campaigns not applied"}, nil
	}
	var result eligibility
	if err := p.db.RPC(ctx, "campaign_for_account", map[string]any{"p_account_id": account}, &result); err != nil {
		return "standard", 0, nil, fmt.Errorf("campaign lookup: %w", err)
	}
	if result.Tier == "" {
		result.Tier = "standard"
	}
	notes := result.Notes
	if result.Campaign != "" {
		notes = append(notes, fmt.Sprintf("campaign %q selected for tier %s", result.Campaign, result.Tier))
	}
	return result.Tier, result.DiscountPct, notes, nil
}
