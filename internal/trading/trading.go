// Package trading reads the conditions a merchant actually trades under: how fast
// each product is moving from the shop's own records, and how much of what it
// sells comes back according to the gateway. It is what an opening quote is argued
// from, so a premium can be explained by something that happened.
package trading

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentmart/internal/negotiation"
	"agentmart/internal/razorpay"
	"agentmart/internal/supabase"
)

// salesWindow is how far back the gateway is asked about, matching the window the
// selling rate view uses so both halves of a quote describe the same period.
const salesWindow = 30 * 24 * time.Hour

// factsTTL is how long a gateway read is reused. Pricing happens inside a
// conversation, so a fresh network round trip per quote would cost the buyer
// seconds to tell the shop something that changes over days.
const factsTTL = 5 * time.Minute

// gatewayReader is the read only slice of the gateway this package needs. It
// exists so the provider can be tested without reaching the network, and it
// cannot express anything that moves money.
type gatewayReader interface {
	SalesFacts(ctx context.Context, since time.Time) (razorpay.SalesFacts, error)
}

// Provider answers what the shop can observe about one product.
type Provider struct {
	db      *supabase.Client
	gateway gatewayReader

	mu     sync.Mutex
	facts  razorpay.SalesFacts
	readAt time.Time
}

// NewProvider constructs a trading provider. A nil db disables it, and a nil
// gateway means the shop prices without refund confidence rather than failing.
func NewProvider(db *supabase.Client, gateway gatewayReader) *Provider {
	if db == nil {
		return nil
	}
	return &Provider{db: db, gateway: gateway}
}

// tradingRow is one row of the selling rate view.
type tradingRow struct {
	ProductID      string `json:"product_id"`
	Stock          int    `json:"stock"`
	UnitsSold      int    `json:"units_sold"`
	StockCoverDays int    `json:"stock_cover_days"`
}

// Conditions reports the trading conditions for one product. A missing row is not
// an error: a product nobody has bought yet has nothing observed about it, and the
// quote is priced without a scarcity premium.
func (p *Provider) Conditions(ctx context.Context, productID string) (negotiation.TradingConditions, error) {
	if p == nil || p.db == nil {
		return negotiation.TradingConditions{}, fmt.Errorf("trading provider is not configured")
	}
	id := strings.TrimSpace(productID)
	if id == "" {
		return negotiation.TradingConditions{}, fmt.Errorf("trading conditions need a product")
	}

	var conditions negotiation.TradingConditions

	var rows []tradingRow
	query := url.Values{
		"product_id": {"eq." + id},
		"select":     {"product_id,stock,units_sold,stock_cover_days"},
		"limit":      {"1"},
	}
	if err := p.db.Get(ctx, "product_trading", query, &rows); err != nil {
		return negotiation.TradingConditions{}, fmt.Errorf("read selling rate: %w", err)
	}
	if len(rows) == 1 {
		conditions.UnitsSold = rows[0].UnitsSold
		conditions.StockCoverDays = rows[0].StockCoverDays
		conditions.Observed = true
	}

	facts, err := p.salesFacts(ctx)
	if err == nil {
		conditions.RefundRatePct = facts.RefundRatePct
		conditions.RefundRateKnown = true
	}
	return conditions, nil
}

// salesFacts returns the gateway's own figures, reusing a recent read. A failure
// is returned so the caller can price without it, never substituted with a guess.
func (p *Provider) salesFacts(ctx context.Context) (razorpay.SalesFacts, error) {
	if p.gateway == nil {
		return razorpay.SalesFacts{}, fmt.Errorf("no gateway reader is configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.readAt.IsZero() && time.Since(p.readAt) < factsTTL {
		return p.facts, nil
	}
	facts, err := p.gateway.SalesFacts(ctx, time.Now().UTC().Add(-salesWindow))
	if err != nil {
		return razorpay.SalesFacts{}, err
	}
	p.facts = facts
	p.readAt = time.Now()
	return facts, nil
}
