// Tests for deterministic merchant counter-offers.
package negotiation

import (
	"testing"

	"agentmart/internal/catalog"
)

func TestPolicyUsesTrustAndStockSignals(t *testing.T) {
	counter, err := (Policy{}).Counter(catalog.Product{PricePaise: 100, WarrantyYears: 1, TrustScore: 92, Stock: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if counter.FinalAmountPaise != 17600 {
		t.Fatalf("counter amount = %d", counter.FinalAmountPaise)
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
