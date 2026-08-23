// Tests for bounded reasoning decisions.
package reasoning

import (
	"context"
	"testing"
)

func TestDeterministicDecisionRespectsSpendLimit(t *testing.T) {
	service, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.Decide(t.Context(), Input{ProductID: "product", Quantity: 2, PricePaise: 600, SpendLimitPaise: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionAskHuman {
		t.Fatalf("action = %q", decision.Action)
	}
}

func TestDeterministicDecisionProducesBuyIntent(t *testing.T) {
	service, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.Decide(t.Context(), Input{ProductID: "product", Quantity: 1, PricePaise: 600, SpendLimitPaise: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionBuy || decision.ProductID != "product" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDeterministicDecisionUsesExactTotal(t *testing.T) {
	service, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.Decide(t.Context(), Input{ProductID: "product", Quantity: 3, PricePaise: 1, TotalPaise: 1200, SpendLimitPaise: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionAskHuman {
		t.Fatalf("action = %q", decision.Action)
	}
}
