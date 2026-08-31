// Proves the price freshness rail is reachable from the purchase path. It used to
// be unreachable, because the caller passed one instant as both the observation
// time and now, so the difference was always zero.
package buyer

import (
	"testing"
	"time"

	"agentmart/internal/gate"
)

// stalePriceWalk runs one purchase whose quote was observed at the given age and
// reports the gate's answer. It uses the real gate, since a fake one would prove
// nothing about the rail.
func stalePriceWalk(t *testing.T, age time.Duration) PurchaseResult {
	t.Helper()
	auditor := &stagedAuditor{}
	moneyGate, err := gate.New(auditor, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewPurchaseService(&stagedShelf{}, &stagedPerson{}, moneyGate, &fakeArtifacts{}, &fakeWallet{}, &stagedApprovals{})
	result, err := service.Purchase(t.Context(), PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         1,
		BaseAmountPaise:  stagedShelfPaise,
		FinalAmountPaise: stagedShelfPaise,
		IdempotencyKey:   "stale-walk",
		PriceObservedAt:  time.Now().UTC().Add(-age),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAQuoteOlderThanTheWindowIsRefusedRatherThanSpent(t *testing.T) {
	result := stalePriceWalk(t, 30*time.Minute)
	if result.Fulfilled {
		t.Fatal("a half hour old quote was spent against")
	}
	if result.Reason != "stale_price" {
		t.Fatalf("reason = %q, want the freshness rail to name itself", result.Reason)
	}
}

func TestAFreshQuoteInsideTheWindowStillBuys(t *testing.T) {
	result := stalePriceWalk(t, 20*time.Second)
	if !result.Fulfilled {
		t.Fatalf("a twenty second old quote was refused: %q", result.Reason)
	}
}

func TestNoObservationTimeIsTreatedAsPricedNow(t *testing.T) {
	auditor := &stagedAuditor{}
	moneyGate, err := gate.New(auditor, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewPurchaseService(&stagedShelf{}, &stagedPerson{}, moneyGate, &fakeArtifacts{}, &fakeWallet{}, &stagedApprovals{})

	// A purchase priced straight from the catalog in this call carries no quote,
	// and must not be refused as stale for having no timestamp.
	result, err := service.Purchase(t.Context(), PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         1,
		BaseAmountPaise:  stagedShelfPaise,
		FinalAmountPaise: stagedShelfPaise,
		IdempotencyKey:   "no-observation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fulfilled {
		t.Fatalf("a catalog priced purchase was refused: %q", result.Reason)
	}
}
