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
