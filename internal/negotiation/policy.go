// Deterministic merchant offer engine: opening offers, combo bundles, floors.
package negotiation

import (
	"fmt"
	"math"

	"agentmart/internal/catalog"
)

// OfferKind classifies what the merchant is actually selling.
type OfferKind string

const (
	// KindUplift is the base product with value-added pricing.
	KindUplift OfferKind = "uplift"
	// KindDiscount is a negotiated price below the opening offer.
	KindDiscount OfferKind = "discount"
	// KindCombo bundles the product with its partner at a percentage off.
	KindCombo OfferKind = "combo"
)

// BundleItem describes one cross-sell attachment in an offer.
type BundleItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PricePaise  int64  `json:"price_paise"` // discounted per-unit price
	DiscountPct int    `json:"discount_pct"`
}

// Offer is a merchant quote for one product and quantity.
type Offer struct {
	Kind       OfferKind
	BasePaise  int64 // main product list price * quantity
	FinalPaise int64 // asking amount
	Reason     string
	Bundle     *BundleItem
}

// Priced pairs a public product with its merchant-private cost basis.
type Priced struct {
	Product   catalog.Product
	CostPaise int64
}

// Policy builds deterministic merchant offers.
type Policy struct{}

// Counter keeps the legacy opening-quote contract used by existing callers.
func (Policy) Counter(product catalog.Product, quantity int) (Counter, error) {
	offer, err := OpeningOffer(Priced{Product: product}, nil, quantity)
	if err != nil {
		return Counter{}, err
	}
	return Counter{FinalAmountPaise: offer.FinalPaise, Reason: offer.Reason}, nil
}

// OpeningOffer quotes the merchant's first ask: list price plus transparent
// uplifts, with the seeded combo attached whenever one is configured.
func OpeningOffer(main Priced, partner *Priced, quantity int) (Offer, error) {
	if quantity <= 0 || main.Product.PricePaise <= 0 || main.Product.PricePaise > math.MaxInt64/int64(quantity) {
		return Offer{}, ErrInvalidProposal
	}
	base := main.Product.PricePaise * int64(quantity)
	final := base
	reason := "standard fulfillment"
	if main.Product.WarrantyYears > 0 {
		final += int64(main.Product.WarrantyYears) * 10_000
		reason = "extended warranty"
	}
	if main.Product.TrustScore >= 90 {
		final += 5_000
		reason += " and high-trust handling"
	}
	if main.Product.Stock > 0 && main.Product.Stock <= quantity {
		final += 2_500
		reason += " and limited stock"
	}
	offer := Offer{Kind: KindUplift, BasePaise: base, FinalPaise: final, Reason: reason}

	pct := main.Product.ComboDiscountPct
	if partner != nil && pct > 0 && pct < 100 && main.Product.ComboWith != nil {
		if partner.Product.PricePaise <= 0 || partner.Product.PricePaise > math.MaxInt64/int64(quantity) {
			return Offer{}, ErrInvalidProposal
		}
		bundleUnit := partner.Product.PricePaise * int64(100-pct) / 100
		if bundleUnit > math.MaxInt64-final {
			return Offer{}, ErrInvalidProposal
		}
		final += bundleUnit * int64(quantity)
		offer.FinalPaise = final
		offer.Kind = KindCombo
		offer.Bundle = &BundleItem{ID: partner.Product.ID, Name: partner.Product.Name, PricePaise: bundleUnit, DiscountPct: pct}
		offer.Reason += fmt.Sprintf(" plus %s at %d%% off", partner.Product.Name, pct)
	}
	return offer, nil
}

// FloorFor returns the minimum acceptable total: blended cost across the main
// product and any bundled partner. The orchestrator never goes below it.
func FloorFor(main Priced, partner *Priced, quantity int) (int64, error) {
	if quantity <= 0 || main.CostPaise < 0 || main.Product.PricePaise > math.MaxInt64/int64(quantity) {
		return 0, ErrInvalidProposal
	}
	total := main.CostPaise * int64(quantity)
	if partner != nil && main.Product.ComboDiscountPct > 0 && main.Product.ComboWith != nil && partner.CostPaise > 0 {
		pct := main.Product.ComboDiscountPct
		bundleCost := partner.CostPaise * int64(100-pct) / 100
		if bundleCost > math.MaxInt64-total {
			return 0, ErrInvalidProposal
		}
		total += bundleCost * int64(quantity)
	}
	return total, nil
}
