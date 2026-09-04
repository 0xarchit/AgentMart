// Tests for the merchant offer engine and loss-free orchestrator.
package negotiation

import (
	"strings"
	"testing"

	"agentmart/internal/catalog"
)

// priced is a product on the shelf. The stock matters for a partner: a bundle is
// only attached when the shop has the partner to give, so a helper without it
// would quietly stop every combo test from having a combo.
func priced(id string, price, cost int64) Priced {
	return Priced{Product: catalog.Product{ID: id, Name: id, PricePaise: price, Stock: 5}, CostPaise: cost}
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

// TestACounterAboveTheAskSettlesAtTheAsk closes the one path where a model wrote
// the settled amount and nothing bounded it. The counter arrives from the buyer's
// negotiating agent, which passes its own amount_paise straight through, and this
// branch returned it verbatim as an accepted price. Everything downstream then
// treated it as the shop's own figure: the session stored it, the buyer recorded
// it as the price the shop agreed to, and the gate approved it because the base
// still matched the unit price and the total was inside the wallet's rails. The
// merchant's own model is clamped to the ask twelve lines further down; the
// buyer's was not.
func TestACounterAboveTheAskSettlesAtTheAsk(t *testing.T) {
	session := counteredSession(100000, 1)
	decision := Decide(session, 250000, 70000)
	if !decision.Accepted {
		t.Fatalf("a buyer offering more than the ask is still an acceptance: %+v", decision)
	}
	if decision.FinalPaise != 100000 {
		t.Fatalf("settled at %d, want the standing ask of 100000. Charging above the ask is what the price guard refuses even the merchant.", decision.FinalPaise)
	}
	// Landing exactly on the ask is unchanged, which is the case the old test covered.
	if exact := Decide(session, 100000, 70000); !exact.Accepted || exact.FinalPaise != 100000 {
		t.Fatalf("meeting the ask exactly = %+v", exact)
	}
}

// A partner nobody can ship must not be charged for. The gate and the fulfilment
// function both check the stock of the named product alone, and the attached goods
// are inside the settled amount, so an out of stock partner was quoted, paid for
// and never allocated. The ask falls back to the main product on its own.
func TestAnEmptyShelfPartnerIsNotAttached(t *testing.T) {
	main := priced("trimmer", 200000, 140000)
	main.Product.ComboDiscountPct = 15
	main.Product.ComboWith = strPtr("cream")
	partner := priced("cream", 45000, 30000)
	partner.Product.Stock = 0

	offer, err := OpeningOffer(main, &partner, 1, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Kind == KindCombo || offer.Bundle != nil || offer.BundledPaise != 0 {
		t.Fatalf("offer = %+v, want no partner attached: the buyer would pay for goods the shop has none of", offer)
	}
	if offer.FinalPaise != 200000 {
		t.Fatalf("ask = %d, want the main product alone at 200000", offer.FinalPaise)
	}

	// One unit short is still short, and the count is what decides it.
	partner.Product.Stock = 1
	two, err := OpeningOffer(main, &partner, 2, TradingConditions{})
	if err != nil {
		t.Fatal(err)
	}
	if two.Bundle != nil {
		t.Fatalf("offer = %+v, want no partner when one unit cannot cover two", two)
	}
}
