// Tests for the merchant graph: bounded money, campaign layer, compilation.
package marketgraph

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
)

func TestClampToRailsNeverSellsBelowFloor(t *testing.T) {
	// Strategy tried to undercut the floor.
	amount, note := clampToRails(60_000, 70_000, 50_000, 0, 100_000)
	if amount != 70_000 || note == "" {
		t.Fatalf("amount = %d, note = %q", amount, note)
	}
	// Strategy tried to undercut the buyer's own bid.
	amount, note = clampToRails(75_000, 70_000, 80_000, 0, 100_000)
	if amount != 80_000 || note == "" {
		t.Fatalf("buyer-bid floor: amount = %d, note = %q", amount, note)
	}
	// Strategy tried to exceed its own standing ask.
	amount, note = clampToRails(150_000, 70_000, 50_000, 0, 100_000)
	if amount != 100_000 || note == "" {
		t.Fatalf("ask ceiling: amount = %d, note = %q", amount, note)
	}
	// Amount already inside the rails passes through unannotated.
	amount, note = clampToRails(90_000, 70_000, 50_000, 0, 100_000)
	if amount != 90_000 || note != "" {
		t.Fatalf("in-rails: amount = %d, note = %q", amount, note)
	}
}

func TestTheConcessionFloorIsEnforcedRatherThanSuggested(t *testing.T) {
	// The schedule says hold at 90000 this round. A strategist conceding straight
	// to the buyer's bid used to be allowed through, because this floor only ever
	// reached the prompt and the audit payload.
	amount, note := clampToRails(72_000, 70_000, 50_000, 90_000, 100_000)
	if amount != 90_000 {
		t.Fatalf("amount = %d, want the round's concession floor held at 90000", amount)
	}
	if !strings.Contains(note, "concession floor") {
		t.Fatalf("note = %q, want it to name the bound that bit", note)
	}
	// The cost floor still wins when it is the higher of the two.
	if amount, _ = clampToRails(60_000, 95_000, 0, 90_000, 100_000); amount != 95_000 {
		t.Fatalf("amount = %d, want the cost floor at 95000", amount)
	}
	// A schedule above the standing ask must not let the merchant charge above it.
	amount, note = clampToRails(80_000, 70_000, 0, 150_000, 100_000)
	if amount != 100_000 {
		t.Fatalf("amount = %d, want the standing ask to cap every floor", amount)
	}
	if !strings.Contains(note, "standing ask") {
		t.Fatalf("note = %q, want the ask named as the bound", note)
	}
}

func TestNewWithoutModelStaysDeterministic(t *testing.T) {
	negotiator, err := New(Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if negotiator != nil {
		t.Fatal("expected nil negotiator so callers keep the concession schedule")
	}
}

func TestNewCompilesMerchantGraph(t *testing.T) {
	negotiator, err := New(Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, nil, nil)
	if err != nil {
		t.Fatalf("build merchant graph: %v", err)
	}
	if negotiator == nil || negotiator.graph == nil {
		t.Fatal("merchant graph missing")
	}
}

func TestNoProviderMeansNoFundedDiscount(t *testing.T) {
	negotiator := &Negotiator{}
	// A shelf holding forty units is not a discount budget. Without a campaign
	// source nothing funds a giveaway, whatever the stock level happens to be.
	for _, stock := range []int{40, 2} {
		tier, pct, notes := negotiator.eligibility(t.Context(), negotiation.CounterInput{
			Product: catalog.Product{Stock: stock},
		})
		if tier != "standard" || pct != 0 {
			t.Fatalf("stock %d gave tier %q pct %d", stock, tier, pct)
		}
		if len(notes) == 0 {
			t.Fatalf("stock %d gave no reason for having no budget", stock)
		}
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

func TestAnOwnerWithNothingToPitchStillAnswers(t *testing.T) {
	event := &session.Event{Output: shopfrontAnswer{
		Greeting: "Nothing on the shelf suits that budget.",
		Options:  nil,
	}}
	answer, err := shopfrontAnswerFrom(event)
	if err != nil {
		t.Fatalf("a refusal in words is an answer: %v", err)
	}
	if answer.Greeting == "" || len(answer.Options) != 0 {
		t.Fatalf("answer = %+v, want the words and no options", answer)
	}
}

func TestAnUnreadableAnswerIsStillAFault(t *testing.T) {
	if _, err := shopfrontAnswerFrom(&session.Event{Output: "not an answer at all"}); err == nil {
		t.Fatal("expected a fault when nothing can be read")
	}
}
