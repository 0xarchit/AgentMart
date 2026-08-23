// Table tests for purchase authorization and mandatory auditing.
package gate

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingAuditor struct {
	decisions []Decision
	err       error
}

func (a *recordingAuditor) RecordGateDecision(_ context.Context, decision Decision) error {
	a.decisions = append(a.decisions, decision)
	return a.err
}

func TestEvaluatePolicies(t *testing.T) {
	now := time.Now()
	valid := Request{AccountID: "account", ProductID: "product", Quantity: 2, UnitPricePaise: 100, BaseAmountPaise: 200, FinalAmountPaise: 200, WalletBalancePaise: 500, SpendLimitPaise: 500, Stock: 3, PriceObservedAt: now, Now: now}
	tests := []struct {
		name     string
		change   func(*Request)
		approved bool
		reason   string
	}{
		{name: "approve", change: func(*Request) {}, approved: true, reason: "approved"},
		{name: "negotiated uplift", change: func(r *Request) { r.FinalAmountPaise = 250 }, approved: true, reason: "approved"},
		{name: "negotiated discount", change: func(r *Request) { r.FinalAmountPaise = 199 }, reason: "negotiated_amount_below_base"},
		{name: "stock", change: func(r *Request) { r.Stock = 1 }, reason: "insufficient_stock"},
		{name: "limit", change: func(r *Request) { r.SpendLimitPaise = 199 }, reason: "human_approval_required"},
		{name: "wallet", change: func(r *Request) { r.WalletBalancePaise = 199 }, reason: "insufficient_wallet_balance"},
		{name: "price", change: func(r *Request) { r.PriceObservedAt = now.Add(-2 * time.Minute) }, reason: "stale_price"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.change(&request)
			auditor := &recordingAuditor{}
			gate, err := New(auditor, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := gate.Evaluate(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Approved != test.approved || decision.Reason != test.reason {
				t.Fatalf("decision = %+v", decision)
			}
			if len(auditor.decisions) != 1 {
				t.Fatalf("audit count = %d", len(auditor.decisions))
			}
		})
	}
}

func TestHumanApprovalCanResumeStalePrice(t *testing.T) {
	gate, err := New(&recordingAuditor{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	decision, err := gate.Evaluate(t.Context(), Request{
		AccountID:          "account",
		ProductID:          "product",
		Quantity:           1,
		UnitPricePaise:     100,
		BaseAmountPaise:    100,
		FinalAmountPaise:   100,
		WalletBalancePaise: 200,
		SpendLimitPaise:    200,
		Stock:              1,
		HumanApproved:      true,
		PriceObservedAt:    now.Add(-2 * time.Minute),
		Now:                now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved || decision.Reason != "approved" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateFailsClosedWhenAuditFails(t *testing.T) {
	auditor := &recordingAuditor{err: errors.New("unavailable")}
	gate, err := New(auditor, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = gate.Evaluate(t.Context(), Request{})
	if err == nil {
		t.Fatal("expected audit failure")
	}
}
