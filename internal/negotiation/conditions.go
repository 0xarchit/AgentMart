// Trading conditions: what the shop can observe about its own business, and the
// bounded uplift those observations justify. The numbers here are bounds, not
// prices. The facts choose within them, so an uplift can be explained by
// something that happened rather than by a constant someone typed.
package negotiation

import (
	"strings"
	"time"
)

// Uplift bounds, in basis points of the list total. These are the only fixed
// numbers in the opening quote: every amount inside them is argued from an
// observation. A basis point is a hundredth of a percent, so 150 is 1.5 percent.
const (
	// upliftCeilingBps caps everything the shop may add to list, whatever the
	// facts say. It is the outermost bound on an opening quote.
	upliftCeilingBps = 1200
	// warrantyBpsPerYear is what one year of cover may add before the refund rate
	// is taken into account.
	warrantyBpsPerYear = 150
	// warrantyCeilingBps stops a long warranty from carrying the whole quote.
	warrantyCeilingBps = 600
	// trustFloorScore is the score at which handling stops being chargeable: a
	// shop cannot bill for being merely average.
	trustFloorScore = 80
	// trustBpsPerPoint is what each point above that floor may add.
	trustBpsPerPoint = 10
	// trustCeilingBps bounds the handling premium.
	trustCeilingBps = 300
	// scarcityBpsWhenTight is what genuine scarcity may add, and it is only
	// available when stock cover has actually been measured.
	scarcityBpsWhenTight = 300
	// scarcityCoverDays is the cover below which stock counts as tight.
	scarcityCoverDays = 7
)

// TradingConditions is what the shop knows about how business is going. Absent
// observations are stated rather than guessed: Observed false means the velocity
// terms are unavailable, not that they are zero.
type TradingConditions struct {
	Since time.Time
	// RefundRatePct is how much of what the shop sells comes back. It is a
	// confidence signal: a shop paying out refunds cannot charge as much for
	// standing behind its goods.
	RefundRatePct int
	// RefundRateKnown reports whether the rate above was actually read. An unproven
	// fact never justifies charging more, so cover is not billed for at all when
	// the shop cannot see what it pays out.
	RefundRateKnown bool
	// AverageCapturePaise is what buyers here actually pay, gateway confirmed.
	AverageCapturePaise int64
	// UnitsSold is how many of this product moved inside the window.
	UnitsSold int
	// StockCoverDays is how long current stock lasts at the observed rate.
	// Negative means it could not be computed.
	StockCoverDays int
	// Observed reports whether the per product velocity above is real.
	Observed bool
}

// UpliftReason explains one component of an opening quote in the shop's own
// terms, so the trail can say why a price is what it is.
type UpliftReason struct {
	Bps   int
	Cause string
}

// UpliftBps returns what may be added to a list total and why, given the product
// signals and what the shop can observe. The result is always within
// upliftCeilingBps.
func UpliftBps(warrantyYears, trustScore int, conditions TradingConditions) (int, []UpliftReason) {
	var reasons []UpliftReason
	total := 0

	// Cover is only chargeable when the shop can see what it pays out. Without that
	// figure the premium has no evidence behind it, and an unproven fact must not
	// license a higher ask.
	if warrantyYears > 0 && conditions.RefundRateKnown {
		bps := warrantyYears * warrantyBpsPerYear
		if bps > warrantyCeilingBps {
			bps = warrantyCeilingBps
		}
		// A shop that pays out refunds is charging for cover it is already
		// spending, so the warranty premium shrinks as the refund rate rises.
		if conditions.RefundRatePct > 0 {
			keep := 100 - conditions.RefundRatePct
			if keep < 0 {
				keep = 0
			}
			bps = bps * keep / 100
		}
		if bps > 0 {
			total += bps
			reasons = append(reasons, UpliftReason{Bps: bps, Cause: "cover on the goods, priced against what comes back"})
		}
	}

	if trustScore > trustFloorScore {
		bps := (trustScore - trustFloorScore) * trustBpsPerPoint
		if bps > trustCeilingBps {
			bps = trustCeilingBps
		}
		total += bps
		reasons = append(reasons, UpliftReason{Bps: bps, Cause: "handling record above the shop's own floor"})
	}

	// Scarcity is only chargeable when it was measured. Stock happening to be low
	// against one order is not evidence that the shelf is running out.
	if conditions.Observed && conditions.StockCoverDays >= 0 && conditions.StockCoverDays <= scarcityCoverDays && conditions.UnitsSold > 0 {
		total += scarcityBpsWhenTight
		reasons = append(reasons, UpliftReason{Bps: scarcityBpsWhenTight, Cause: "stock is inside a week of cover at the current rate"})
	}

	if total > upliftCeilingBps {
		total = upliftCeilingBps
		reasons = append(reasons, UpliftReason{Bps: 0, Cause: "held at the shop's own ceiling on any uplift"})
	}
	return total, reasons
}

// applyBps returns the amount that bps of a base represents, rounded down so an
// uplift can never round its way above its own bound.
func applyBps(basePaise int64, bps int) int64 {
	if basePaise <= 0 || bps <= 0 {
		return 0
	}
	return basePaise * int64(bps) / 10_000
}

// reasonFrom turns the priced causes into one line the shop can say out loud.
func reasonFrom(reasons []UpliftReason) string {
	causes := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		causes = append(causes, reason.Cause)
	}
	if len(causes) == 0 {
		return "list price, nothing added"
	}
	return strings.Join(causes, ", and ")
}
