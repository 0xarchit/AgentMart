// Tests for deterministic merchant counter-offers.
package negotiation

import (
	"testing"

	"agentmart/internal/catalog"
)

func TestAnUpliftIsProportionateToTheListPrice(t *testing.T) {
	// The same signals on a cheap and an expensive item must add the same share of
	// the list total, not the same number of paise. Flat uplifts quoted a one rupee
	// item at a hundred and seventy six times its own price.
	signals := catalog.Product{WarrantyYears: 1, TrustScore: 92, Stock: 1}
	seen := TradingConditions{RefundRateKnown: true}

	cheap := signals
	cheap.PricePaise = 100
	cheapOffer, err := OpeningOffer(Priced{Product: cheap}, nil, 1, seen)
	if err != nil {
		t.Fatal(err)
	}

	dear := signals
	dear.PricePaise = 100_000
	dearOffer, err := OpeningOffer(Priced{Product: dear}, nil, 1, seen)
	if err != nil {
		t.Fatal(err)
	}

	// One year of cover is 150 basis points and twelve trust points above the floor
	// are 120, so both asks carry 270 basis points over list.
	if cheapOffer.FinalPaise != 102 {
		t.Fatalf("cheap ask = %d, want 102", cheapOffer.FinalPaise)
	}
	if dearOffer.FinalPaise != 102_700 {
		t.Fatalf("dear ask = %d, want 102700", dearOffer.FinalPaise)
	}
}

func TestCoverIsNotBilledForWhenPayoutsCannotBeSeen(t *testing.T) {
	product := catalog.Product{PricePaise: 100_000, WarrantyYears: 3}

	blind, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	if blind.FinalPaise != 100_000 {
		t.Fatalf("ask = %d, want the list total when nothing is observed", blind.FinalPaise)
	}

	seen, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{RefundRateKnown: true})
	if err != nil {
		t.Fatal(err)
	}
	if seen.FinalPaise != 104_500 {
		t.Fatalf("ask = %d, want 450 basis points for three years of cover", seen.FinalPaise)
	}
}

func TestScarcityIsChargedOnlyWhenItWasMeasured(t *testing.T) {
	product := catalog.Product{PricePaise: 100_000, Stock: 1}

	// Stock of one against an order of one is not evidence the shelf is emptying.
	unobserved, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	if unobserved.FinalPaise != 100_000 {
		t.Fatalf("unobserved ask = %d, want the list total", unobserved.FinalPaise)
	}

	measured, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{Observed: true, UnitsSold: 9, StockCoverDays: 2})
	if err != nil {
		t.Fatal(err)
	}
	if measured.FinalPaise != 103_000 {
		t.Fatalf("measured ask = %d, want 300 basis points of scarcity", measured.FinalPaise)
	}
}

func TestRefundsPaidOutShrinkWhatCoverCanBeSoldFor(t *testing.T) {
	product := catalog.Product{PricePaise: 100_000, WarrantyYears: 4}

	clean, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{RefundRateKnown: true})
	if err != nil {
		t.Fatal(err)
	}
	leaky, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{RefundRateKnown: true, RefundRatePct: 50})
	if err != nil {
		t.Fatal(err)
	}
	if leaky.FinalPaise >= clean.FinalPaise {
		t.Fatalf("a shop refunding half its sales asked %d, not less than %d", leaky.FinalPaise, clean.FinalPaise)
	}
}

func TestNoSignalCanCarryAnAskPastTheCeiling(t *testing.T) {
	// Every bound at once: a long warranty, a perfect record and measured scarcity.
	product := catalog.Product{PricePaise: 100_000, WarrantyYears: 99, TrustScore: 100}
	offer, err := OpeningOffer(Priced{Product: product}, nil, 1, TradingConditions{RefundRateKnown: true, Observed: true, UnitsSold: 40, StockCoverDays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if offer.FinalPaise > 112_000 {
		t.Fatalf("ask = %d, want no more than 1200 basis points over list", offer.FinalPaise)
	}
}

func TestNoEntitlementKeepsTheFloorAtTheListTotal(t *testing.T) {
	// Every buyer without a campaign gets exactly the behaviour this system had
	// before a discount was representable at all.
	if floor := EntitledFloor(60_000, 100_000, 0); floor != 100_000 {
		t.Fatalf("floor = %d, want the list total held at 100000", floor)
	}
}

func TestAnEntitlementMovesTheFloorButNeverBelowCost(t *testing.T) {
	if floor := EntitledFloor(60_000, 100_000, 12); floor != 88_000 {
		t.Fatalf("floor = %d, want 12 percent off the list total", floor)
	}
	// The cost floor is absolute: a generous entitlement cannot sell at a loss.
	if floor := EntitledFloor(95_000, 100_000, 40); floor != 95_000 {
		t.Fatalf("floor = %d, want the cost floor to win", floor)
	}
}

func TestAnAbsurdEntitlementIsIgnoredRatherThanTrusted(t *testing.T) {
	for _, pct := range []int{-5, 100, 250} {
		if floor := EntitledFloor(10_000, 100_000, pct); floor != 100_000 {
			t.Fatalf("entitlement %d gave floor %d, want the list total", pct, floor)
		}
	}
}
