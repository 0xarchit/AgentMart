// The shop's opening turn: the buyer agent asks what the merchant has, and the
// merchant answers with a pitched shortlist instead of a raw catalog dump.
package negotiation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentmart/internal/catalog"
)

// BrowseInput is what the merchant's shop-owner reasoning receives.
type BrowseInput struct {
	Brief       string
	BudgetPaise int64
	AccountID   string
	Candidates  []catalog.Product
}

// BrowseOption is one pitched product. Name and PricePaise are authoritative:
// they are re-attached from the catalog row, never taken from the model.
type BrowseOption struct {
	ProductID     string `json:"product_id"`
	Name          string `json:"name"`
	PricePaise    int64  `json:"price_paise"`
	Pitch         string `json:"pitch"`
	Includes      string `json:"includes,omitempty"`
	Stock         int    `json:"stock,omitempty"`
	WarrantyYears int    `json:"warranty_years,omitempty"`
	TrustScore    int    `json:"trust_score,omitempty"`
}

// BrowseOutput is the shortlist the merchant shows.
type BrowseOutput struct {
	Greeting string         `json:"greeting"`
	Options  []BrowseOption `json:"options"`
	Closing  string         `json:"closing,omitempty"`
}

// Shopfront is the merchant's shop-owner voice. Implemented by the merchant
// reasoning layer; declared here so this package does not import it.
type Shopfront interface {
	Shortlist(ctx context.Context, input BrowseInput) (BrowseOutput, error)
}

// browse runs the shop's opening turn and returns the shortlist with the two
// transcript turns that started the conversation.
func (s *Server) browse(ctx context.Context, request negotiationRequest) (map[string]any, error) {
	brief := strings.TrimSpace(request.Brief)
	if brief == "" {
		return nil, fmt.Errorf("brief is required to browse")
	}
	if s.shopfront == nil || s.search == nil {
		return nil, fmt.Errorf("this merchant cannot show a shortlist")
	}

	candidates, err := s.search(ctx, catalog.SearchRequest{
		Query:         brief,
		MaxPricePaise: request.BudgetPaise,
	})
	if err != nil {
		return nil, fmt.Errorf("look through stock: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("nothing in stock matches %q within the budget", brief)
	}

	shortlist, err := s.shopfront.Shortlist(ctx, BrowseInput{
		Brief:       brief,
		BudgetPaise: request.BudgetPaise,
		AccountID:   strings.TrimSpace(request.AccountID),
		Candidates:  candidates,
	})
	if err != nil {
		return nil, fmt.Errorf("shop owner could not answer: %w", err)
	}
	shortlist.Options = withCatalogTruth(shortlist.Options, candidates)
	if len(shortlist.Options) == 0 {
		return nil, fmt.Errorf("shop owner named no product that is actually in stock")
	}

	now := time.Now().UTC()
	transcript := []Turn{
		{Actor: "buyer", Message: buyerOpening(brief, request.BudgetPaise), At: now},
		{Actor: "merchant", Message: merchantOpening(shortlist), At: now},
	}
	return map[string]any{
		"type":       "shortlist",
		"greeting":   shortlist.Greeting,
		"options":    shortlist.Options,
		"closing":    shortlist.Closing,
		"transcript": transcript,
	}, nil
}

// withCatalogTruth keeps only options that name a real candidate and overwrites
// every money and stock field from the catalog row, so no displayed number can
// come from the model.
func withCatalogTruth(options []BrowseOption, candidates []catalog.Product) []BrowseOption {
	rows := make(map[string]catalog.Product, len(candidates))
	for _, candidate := range candidates {
		rows[candidate.ID] = candidate
	}
	seen := make(map[string]bool, len(options))
	var kept []BrowseOption
	for _, option := range options {
		row, ok := rows[strings.TrimSpace(option.ProductID)]
		if !ok || seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		option.ProductID = row.ID
		option.Name = row.Name
		option.PricePaise = row.PricePaise
		option.Stock = row.Stock
		option.WarrantyYears = row.WarrantyYears
		option.TrustScore = row.TrustScore
		kept = append(kept, option)
	}
	return kept
}

func buyerOpening(brief string, budgetPaise int64) string {
	if budgetPaise > 0 {
		return fmt.Sprintf("Looking for %s, budget around INR %.2f. What do you have?", brief, float64(budgetPaise)/100)
	}
	return fmt.Sprintf("Looking for %s. What do you have?", brief)
}

func merchantOpening(shortlist BrowseOutput) string {
	var builder strings.Builder
	if greeting := strings.TrimSpace(shortlist.Greeting); greeting != "" {
		builder.WriteString(greeting)
	}
	for _, option := range shortlist.Options {
		fmt.Fprintf(&builder, "\n- %s at INR %.2f: %s", option.Name, float64(option.PricePaise)/100, option.Pitch)
	}
	if closing := strings.TrimSpace(shortlist.Closing); closing != "" {
		builder.WriteString("\n" + closing)
	}
	return builder.String()
}
