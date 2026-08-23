// Tests for the negotiation state machine.
package negotiation

import "testing"

func TestNegotiationAcceptsCounterAndComputesUplift(t *testing.T) {
	session, err := New(Proposal{ProductID: "product", Quantity: 1, BaseAmountPaise: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CounterOffer(Counter{FinalAmountPaise: 140, Reason: "warranty"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Accept(); err != nil {
		t.Fatal(err)
	}
	if session.UpliftPaise() != 40 {
		t.Fatalf("uplift = %d", session.UpliftPaise())
	}
}

func TestNegotiationRejectsInvalidTransitions(t *testing.T) {
	session, _ := New(Proposal{ProductID: "product", Quantity: 1, BaseAmountPaise: 100})
	if err := session.Accept(); err == nil {
		t.Fatal("expected transition error")
	}
	if err := session.CounterOffer(Counter{FinalAmountPaise: 90, Reason: "discount"}); err == nil {
		t.Fatal("expected below-base error")
	}
}
