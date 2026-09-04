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
	// BundledPaise is the discounted partner total already inside FinalPaise.
	// Derived here rather than by each caller multiplying the per-unit bundle
	// price again, because a funded buyer's floor is a share of the list total of
	// everything being bought and getting that arithmetic twice invites the two
	// answers to drift.
	BundledPaise int64
}

// Priced pairs a public product with its merchant-private cost basis.
type Priced struct {
	Product   catalog.Product
	CostPaise int64
}

// Policy builds deterministic merchant offers.
type Policy struct{}

// Counter keeps the legacy opening-quote contract used by existing callers. It
// quotes with nothing observed, which is the conservative case: no velocity means
// no scarcity premium.
func (Policy) Counter(product catalog.Product, quantity int) (Counter, error) {
	offer, err := OpeningOffer(Priced{Product: product}, nil, quantity, TradingConditions{})
	if err != nil {
		return Counter{}, err
	}
	return Counter{FinalAmountPaise: offer.FinalPaise, Reason: offer.Reason}, nil
}

// OpeningOffer quotes the merchant's first ask: the list total plus whatever
// uplift the shop's own trading conditions justify, with the seeded combo
// attached whenever one is configured. Every added amount is a share of the list
// total argued from an observation, so the same product carries a proportionate
// uplift whether it costs hundreds or thousands.
func OpeningOffer(main Priced, partner *Priced, quantity int, conditions TradingConditions) (Offer, error) {
	if quantity <= 0 || main.Product.PricePaise <= 0 || main.Product.PricePaise > math.MaxInt64/int64(quantity) {
		return Offer{}, ErrInvalidProposal
	}
	base := main.Product.PricePaise * int64(quantity)
	bps, reasons := UpliftBps(main.Product.WarrantyYears, main.Product.TrustScore, conditions)
	final := base + applyBps(base, bps)
	reason := reasonFrom(reasons)
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
		offer.BundledPaise = bundleUnit * int64(quantity)
		final += offer.BundledPaise
		offer.FinalPaise = final
		offer.Kind = KindCombo
		offer.Bundle = &BundleItem{ID: partner.Product.ID, Name: partner.Product.Name, PricePaise: bundleUnit, DiscountPct: pct}
		offer.Reason += fmt.Sprintf(" plus %s at %d%% off", partner.Product.Name, pct)
	}
	return offer, nil
}

// EntitledFloor is how far below the list total the merchant may go for one
// buyer. A funded discount is the only thing that moves the floor under list, and
// the cost floor is absolute regardless, so an entitlement can never sell at a
// loss. A zero entitlement returns the list total, which is what every buyer
// without a campaign gets and what this system did for everyone before.
func EntitledFloor(costFloorPaise, listPaise int64, entitlementPct int) int64 {
	floor := listPaise
	if entitlementPct > 0 && entitlementPct < 100 && listPaise > 0 {
		floor = listPaise - (listPaise * int64(entitlementPct) / 100)
	}
	if costFloorPaise > floor {
		return costFloorPaise
	}
	return floor
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
