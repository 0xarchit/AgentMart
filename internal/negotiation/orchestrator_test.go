// Tests for the merchant offer engine and loss-free orchestrator.
package negotiation

import (
	"strings"
	"testing"

	"agentmart/internal/catalog"
)

func priced(id string, price, cost int64) Priced {
	return Priced{Product: catalog.Product{ID: id, Name: id, PricePaise: price}, CostPaise: cost}
}

func TestAnOpeningOfferNeverStartsBelowList(t *testing.T) {
	offer, err := OpeningOffer(priced("p", 100, 70), nil, 1, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	// No warranty, no trust above the floor and nothing observed, so the ask is the
	// list total exactly.
	if offer.Kind != KindUplift || offer.FinalPaise != offer.BasePaise {
		t.Fatalf("offer = %+v", offer)
	}
}

func TestOpeningOfferAttachesCombo(t *testing.T) {
	main := priced("trimmer", 200000, 140000)
	main.Product.ComboDiscountPct = 15
	main.Product.ComboWith = strPtr("cream")
	partner := priced("cream", 45000, 30000)

	floor, err := FloorFor(main, &partner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if floor != 140000+30000*85/100 {
		t.Fatalf("combo floor = %d", floor)
	}

	offer, err := OpeningOffer(main, &partner, 1, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Kind != KindCombo || offer.Bundle == nil || !strings.Contains(offer.Reason, "cream") {
		t.Fatalf("offer = %+v", offer)
	}
	if offer.Bundle.PricePaise != 45000*85/100 {
		t.Fatalf("bundle unit = %d", offer.Bundle.PricePaise)
	}
	if offer.FinalPaise <= floor {
		t.Fatalf("opening below floor: %d <= %d", offer.FinalPaise, floor)
	}
}

func TestOrchestratorAcceptsWithinMargin(t *testing.T) {
	session := counteredSession(100000, 1)
	// Round 1 holds at ask; a full-price buyer is accepted immediately.
	d := Decide(session, 100000, 70000)
	if !d.Accepted || d.FinalPaise != 100000 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestOrchestratorNeverCrossesFloor(t *testing.T) {
	session := counteredSession(100000, MaxRounds) // all rounds spent
	d := Decide(session, 50000, 70000)
	if d.Accepted || d.FinalPaise != 70000 || !d.Exhausted {
		t.Fatalf("decision = %+v", d)
	}
}

func TestOrchestratorConcedesTowardFloor(t *testing.T) {
	session := counteredSession(100000, 2)
	d := Decide(session, 80000, 70000)
	if d.Accepted {
		t.Fatalf("round-2 minimum is above 80000: %+v", d)
	}
	if d.FinalPaise < 80000 || d.FinalPaise > 100000 {
		t.Fatalf("counter outside rails: %d", d.FinalPaise)
	}

	session.Round = MaxRounds // final round demands only ~15% of span above floor
	d = Decide(session, 74500, 70000)
	if !d.Accepted || d.FinalPaise != 74500 {
		t.Fatalf("final-round accept failed: %+v", d)
	}
	d = Decide(session, 72000, 70000)
	if d.Accepted || !d.Exhausted {
		t.Fatalf("below-minimum on last round should exhaust: %+v", d)
	}
}

func TestRenegotiateRespectsRoundCap(t *testing.T) {
	session := counteredSession(100000, MaxRounds)
	err := session.Renegotiate(Counter{FinalAmountPaise: 90000, Reason: "one more"})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected round-limit error, got %v", err)
	}
}

func counteredSession(askPaise int64, rounds int) Session {
	s, _ := New(Proposal{ProductID: "p", Quantity: 1, BaseAmountPaise: askPaise / 2})
	s.Counter = Counter{FinalAmountPaise: askPaise, Reason: "opening"}
	s.Status = StatusCountered
	s.Round = rounds
	return s
}

func strPtr(v string) *string { return &v }
