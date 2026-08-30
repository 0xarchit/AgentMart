// The merchant's shop-owner voice: turns candidate stock into a pitched
// shortlist. The model writes only prose; every number stays with the catalog.
package marketgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"agentmart/internal/catalog"
	"agentmart/internal/failure"
	"agentmart/internal/llmchat"
	"agentmart/internal/negotiation"
)

const shopfrontTimeout = 90 * time.Second

const shopfrontInstruction = `You own this shop and a buyer just walked in and
asked what you have. You receive their brief, the ceiling they mentioned, and
the products you actually hold, each with its price, warranty, trust score,
stock, and any partner product you like to sell alongside it.

Pick two to four you would genuinely put in front of this person and sell them
the way an owner does: what it is good for, why it is worth the money, what is
included. Lead with the one you believe suits the brief best. If a partner
product makes an option better value together, say so in that option's pitch.
If stock is thin on something good, it is fair to mention it.

Never invent a product: every product_id must be one you were shown. Do not
state prices in your prose; the price is attached from the shop's own records.

Return only JSON:
{"greeting":"one line, how you greet them",
 "options":[{"product_id":"...","pitch":"one or two sentences selling it",
             "includes":"what comes with it, or empty"}],
 "closing":"one line inviting them to pick or ask for a price"}`

// shopfrontBrief is what the shop-owner reasoning is shown. Money and stock
// facts come straight from the catalog rows.
type shopfrontBrief struct {
	Brief       string           `json:"brief"`
	BudgetPaise int64            `json:"budget_paise"`
	Loyalty     string           `json:"buyer_standing,omitempty"`
	Products    []shopfrontStock `json:"products"`
}

type shopfrontStock struct {
	ProductID        string `json:"product_id"`
	Name             string `json:"name"`
	Category         string `json:"category"`
	PricePaise       int64  `json:"price_paise"`
	Stock            int    `json:"stock"`
	WarrantyYears    int    `json:"warranty_years"`
	TrustScore       int    `json:"trust_score"`
	PartnerProductID string `json:"partner_product_id,omitempty"`
	PartnerDiscount  int    `json:"partner_discount_pct,omitempty"`
}

// shopfrontAnswer is the model's half of the shortlist: prose only.
type shopfrontAnswer struct {
	Greeting string `json:"greeting"`
	Options  []struct {
		ProductID string `json:"product_id"`
		Pitch     string `json:"pitch"`
		Includes  string `json:"includes,omitempty"`
	} `json:"options"`
	Closing string `json:"closing"`
}

// buildShopfront creates the shop-owner agent.
func buildShopfront(cfg Config) (agent.Agent, error) {
	schema, err := llmchat.SchemaFor[shopfrontAnswer]()
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:         "shopfront-agent",
		Description:  "Greets a buyer and pitches the stock worth showing them.",
		Model:        llmchat.New(cfg.Model, cfg.APIKey, cfg.BaseURL),
		Instruction:  shopfrontInstruction,
		OutputSchema: schema,
	})
}

// Shortlist implements negotiation.Shopfront: the owner decides what to show and
// how to sell it, and the catalog decides what everything costs.
func (n *Negotiator) Shortlist(parent context.Context, input negotiation.BrowseInput) (negotiation.BrowseOutput, error) {
	if n == nil || n.shopfront == nil {
		return negotiation.BrowseOutput{}, failure.Reasoning(fmt.Errorf("shop owner reasoning is not configured"))
	}
	if len(input.Candidates) == 0 {
		return negotiation.BrowseOutput{}, fmt.Errorf("no stock to show")
	}

	brief := shopfrontBrief{
		Brief:       input.Brief,
		BudgetPaise: input.BudgetPaise,
		Loyalty:     n.buyerStanding(parent, input.AccountID),
		Products:    stockFacts(input.Candidates),
	}
	payload, err := json.Marshal(brief)
	if err != nil {
		return negotiation.BrowseOutput{}, err
	}

	answer, err := n.runShopfront(parent, string(payload))
	if err != nil {
		return negotiation.BrowseOutput{}, err
	}

	out := negotiation.BrowseOutput{Greeting: answer.Greeting, Closing: answer.Closing}
	for _, option := range answer.Options {
		out.Options = append(out.Options, negotiation.BrowseOption{
			ProductID: strings.TrimSpace(option.ProductID),
			Pitch:     strings.TrimSpace(option.Pitch),
			Includes:  strings.TrimSpace(option.Includes),
		})
	}
	return out, nil
}

// buyerStanding describes the buyer's campaign tier in words the owner can use.
// A caller without an account is simply a new face.
func (n *Negotiator) buyerStanding(ctx context.Context, accountID string) string {
	if accountID == "" || n.campaigns == nil {
		return "new customer, no order history"
	}
	tier, discountPct, _, err := n.campaigns.Eligibility(ctx,
		negotiation.CounterInput{Session: negotiation.Session{BuyerAccountID: accountID}})
	if err != nil || tier == "" {
		return "returning customer"
	}
	if discountPct > 0 {
		return fmt.Sprintf("%s, you may fund up to %d percent off", tier, discountPct)
	}
	return tier
}

func stockFacts(candidates []catalog.Product) []shopfrontStock {
	facts := make([]shopfrontStock, 0, len(candidates))
	for _, product := range candidates {
		fact := shopfrontStock{
			ProductID: product.ID, Name: product.Name, Category: product.Category,
			PricePaise: product.PricePaise, Stock: product.Stock,
			WarrantyYears: product.WarrantyYears, TrustScore: product.TrustScore,
			PartnerDiscount: product.ComboDiscountPct,
		}
		if product.ComboWith != nil {
			fact.PartnerProductID = *product.ComboWith
		}
		facts = append(facts, fact)
	}
	return facts
}

func (n *Negotiator) runShopfront(parent context.Context, payload string) (shopfrontAnswer, error) {
	run, err := runner.NewInMemory("agentmart-shopfront", n.shopfront)
	if err != nil {
		return shopfrontAnswer{}, fmt.Errorf("shop owner runner: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, shopfrontTimeout)
	defer cancel()

	var last *session.Event
	for event, runErr := range run.Run(ctx, "merchant",
		fmt.Sprintf("shopfront-%d", n.sessions.Add(1)), genai.NewContentFromText(payload, genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			return shopfrontAnswer{}, failure.Reasoning(runErr)
		}
		if event != nil {
			last = event
		}
	}
	return shopfrontAnswerFrom(last)
}

func shopfrontAnswerFrom(event *session.Event) (shopfrontAnswer, error) {
	if event == nil {
		return shopfrontAnswer{}, fmt.Errorf("shop owner said nothing")
	}
	if encoded, err := json.Marshal(event.Output); err == nil {
		var out shopfrontAnswer
		if json.Unmarshal(encoded, &out) == nil && len(out.Options) > 0 {
			return out, nil
		}
	}
	if event.Content != nil {
		for _, part := range event.Content.Parts {
			if part == nil || strings.TrimSpace(part.Text) == "" {
				continue
			}
			var out shopfrontAnswer
			if json.Unmarshal([]byte(part.Text), &out) == nil && len(out.Options) > 0 {
				return out, nil
			}
		}
	}
	return shopfrontAnswer{}, fmt.Errorf("shop owner named no product")
}
