// Deterministic merchant policy for a first counter-offer.
package negotiation

import (
	"math"

	"agentmart/internal/catalog"
)

// Policy chooses a transparent uplift from product metadata.
type Policy struct{}

// Counter builds a counter-offer for one product and quantity.
func (Policy) Counter(product catalog.Product, quantity int) (Counter, error) {
	if quantity <= 0 || product.PricePaise <= 0 || product.PricePaise > math.MaxInt64/int64(quantity) {
		return Counter{}, ErrInvalidProposal
	}
	proposal := product.PricePaise * int64(quantity)
	uplift := int64(0)
	reason := "standard fulfillment"
	if product.WarrantyYears > 0 {
		uplift += int64(product.WarrantyYears) * 10_000
		reason = "extended warranty"
	}
	if product.TrustScore >= 90 {
		uplift += 5_000
		reason += " and high-trust handling"
	}
	if product.Stock <= quantity {
		uplift += 2_500
		reason += " and limited stock"
	}
	return Counter{FinalAmountPaise: proposal + uplift, Reason: reason}, nil
}
