// Tests for the merchant graph: bounded money, campaign layer, compilation.
package marketgraph

import (
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
)

func TestClampToRailsNeverSellsBelowFloor(t *testing.T) {
	// Strategy tried to undercut the floor.
	amount, note := clampToRails(60_000, 70_000, 50_000, 100_000)
	if amount != 70_000 || note == "" {
		t.Fatalf("amount = %d, note = %q", amount, note)
	}
	// Strategy tried to undercut the buyer's own bid.
	amount, note = clampToRails(75_000, 70_000, 80_000, 100_000)
	if amount != 80_000 || note == "" {
		t.Fatalf("buyer-bid floor: amount = %d, note = %q", amount, note)
	}
	// Strategy tried to exceed its own standing ask.
	amount, note = clampToRails(150_000, 70_000, 50_000, 100_000)
	if amount != 100_000 || note == "" {
		t.Fatalf("ask ceiling: amount = %d, note = %q", amount, note)
	}
	// Amount already inside the rails passes through unannotated.
	amount, note = clampToRails(90_000, 70_000, 50_000, 100_000)
	if amount != 90_000 || note != "" {
		t.Fatalf("in-rails: amount = %d, note = %q", amount, note)
	}
}

func TestNewWithoutModelStaysDeterministic(t *testing.T) {
	negotiator, err := New(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if negotiator != nil {
		t.Fatal("expected nil negotiator so callers keep the concession schedule")
	}
}

func TestNewCompilesMerchantGraph(t *testing.T) {
	negotiator, err := New(Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, nil)
	if err != nil {
		t.Fatalf("build merchant graph: %v", err)
	}
	if negotiator == nil || negotiator.graph == nil {
		t.Fatal("merchant graph missing")
	}
}

func TestEligibilityUsesStockSignalsWithoutProvider(t *testing.T) {
	negotiator := &Negotiator{}
	tier, pct, notes := negotiator.eligibility(t.Context(), negotiation.CounterInput{
		Product: catalog.Product{Stock: 40},
	})
	if tier != "stock_clearance" || pct != 5 || len(notes) == 0 {
		t.Fatalf("clearance tier = %q pct = %d notes = %v", tier, pct, notes)
	}
	tier, pct, _ = negotiator.eligibility(t.Context(), negotiation.CounterInput{
		Product: catalog.Product{Stock: 2},
	})
	if tier != "scarce" || pct != 0 {
		t.Fatalf("scarce tier = %q pct = %d", tier, pct)
	}
}

func TestFactsFromCarriesRailsAndTranscript(t *testing.T) {
	session, err := negotiation.New(negotiation.Proposal{ProductID: "p", Quantity: 1, BaseAmountPaise: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CounterOffer(negotiation.Counter{FinalAmountPaise: 120, Reason: "warranty"}); err != nil {
		t.Fatal(err)
	}
	partner := catalog.Product{Name: "CalmSkin Cream"}
	facts := factsFrom(negotiation.CounterInput{
		Session: session, Product: catalog.Product{Name: "TrimPro", Stock: 5, TrustScore: 92},
		Partner: &partner, FloorPaise: 70, AskPaise: 120, BuyerPaise: 90, MinAcceptablePaise: 100,
	})
	if facts.FloorPaise != 70 || facts.AskPaise != 120 || facts.MinAcceptablePaise != 100 {
		t.Fatalf("rails lost: %+v", facts)
	}
	if facts.BundleName != "CalmSkin Cream" || len(facts.Transcript) == 0 {
		t.Fatalf("bundle/transcript lost: %+v", facts)
	}
}
