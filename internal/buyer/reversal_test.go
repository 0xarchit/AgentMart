// The reversal leaves a gateway record of a cancellation and moves no money. What
// it used to do, refund the captured top-ups that funded the allowance, returned
// the cancelled amount a second time: the credit had already made it spendable.
// These pin the record, and that nothing about it can be spent.
package buyer

import (
	"context"
	"fmt"
	"testing"

	"agentmart/internal/razorpay"
)

type reversalRecorder struct {
	calls   int
	amounts []int64
	receipt string
	notes   map[string]string
	fail    bool
}

func (r *reversalRecorder) CreateWalletArtifact(_ context.Context, amountPaise int64, receipt string, notes map[string]string) (razorpay.Order, error) {
	if r.fail {
		return razorpay.Order{}, fmt.Errorf("gateway refused")
	}
	r.calls++
	r.amounts = append(r.amounts, amountPaise)
	r.receipt = receipt
	r.notes = notes
	return razorpay.Order{ID: "order_reversal"}, nil
}

func TestACancellationIsRecordedOnceForTheWholeAmount(t *testing.T) {
	gateway := &reversalRecorder{}
	result, err := NewGatewayReversal(gateway).Reverse(t.Context(), ReverseRequest{
		AccountID: "account", OrderID: "order-7", AmountPaise: 45_000,
		IdempotencyKey: "telegram:1:refund:2", Reason: "Cancelled by user", RunID: "run-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	// One record, for the credited amount, and no second one: a cancellation that
	// reached two gateway calls would be reversing money twice if either moved any.
	if gateway.calls != 1 || len(gateway.amounts) != 1 || gateway.amounts[0] != 45_000 {
		t.Fatalf("gateway called %d times with %v, want one record of 45000", gateway.calls, gateway.amounts)
	}
	if result.ReversedPaise != 45_000 || len(result.RefundIDs) != 1 || result.RefundIDs[0] != "order_reversal" {
		t.Fatalf("result = %+v, want the whole amount and the record it left", result)
	}
}

// The record has to explain itself without anything of ours beside it, and the run
// it names is the one the first attempt used rather than the one it is replayed in.
func TestTheRecordCarriesTheOrderAndTheConversation(t *testing.T) {
	gateway := &reversalRecorder{}
	if _, err := NewGatewayReversal(gateway).Reverse(t.Context(), ReverseRequest{
		AccountID: "account", OrderID: "order-7", AmountPaise: 45_000,
		IdempotencyKey: "telegram:1:refund:2", Reason: "Cancelled by user", RunID: "run-9",
	}); err != nil {
		t.Fatal(err)
	}
	if gateway.notes["order_id"] != "order-7" || gateway.notes["run_id"] != "run-9" || gateway.notes["reason"] != "Cancelled by user" {
		t.Fatalf("notes = %v, want the cancelled order and the run the request names", gateway.notes)
	}
	if gateway.notes["account_id"] != "account" || gateway.notes["returned"] != "wallet_allowance" {
		t.Fatalf("notes = %v, want the record to say where the money went back to", gateway.notes)
	}
	// Prefixed, so the record is never mistaken for the purchase it reverses, and
	// derived from the credit's own key so the two line up.
	if gateway.receipt != "reversal_telegram:1:refund:2" {
		t.Fatalf("receipt = %q, want the credit key under a reversal prefix", gateway.receipt)
	}
}

func TestALongRefundKeyStillYieldsAnAcceptableReceipt(t *testing.T) {
	gateway := &reversalRecorder{}
	if _, err := NewGatewayReversal(gateway).Reverse(t.Context(), ReverseRequest{
		AccountID: "account", OrderID: "order-7", AmountPaise: 1,
		IdempotencyKey: "telegram:100000000000000000:refund:200000000000000000",
	}); err != nil {
		t.Fatal(err)
	}
	if len(gateway.receipt) != 40 {
		t.Fatalf("receipt is %d characters, want the gateway's forty", len(gateway.receipt))
	}
}

func TestAGatewayRefusalIsAnErrorAndNotAnEmptyRecord(t *testing.T) {
	result, err := NewGatewayReversal(&reversalRecorder{fail: true}).Reverse(t.Context(), ReverseRequest{
		AccountID: "account", OrderID: "order-7", AmountPaise: 45_000, IdempotencyKey: "key",
	})
	if err == nil {
		t.Fatal("a gateway refusal should be reported, so the attempt stays owed")
	}
	if len(result.RefundIDs) != 0 || result.ReversedPaise != 0 {
		t.Fatalf("result = %+v, want nothing claimed when nothing was recorded", result)
	}
}

func TestAReversalRefusesWithoutWhatItNeeds(t *testing.T) {
	reversal := NewGatewayReversal(&reversalRecorder{})

	if _, err := (&GatewayReversal{}).Reverse(t.Context(), ReverseRequest{AccountID: "a", OrderID: "o", AmountPaise: 1}); err == nil {
		t.Fatal("a reversal with no path should refuse")
	}
	if _, err := reversal.Reverse(t.Context(), ReverseRequest{OrderID: "o", AmountPaise: 1}); err == nil {
		t.Fatal("a reversal with no account should refuse")
	}
	if _, err := reversal.Reverse(t.Context(), ReverseRequest{AccountID: "a", AmountPaise: 1}); err == nil {
		t.Fatal("a reversal with no order should refuse")
	}
	if _, err := reversal.Reverse(t.Context(), ReverseRequest{AccountID: "a", OrderID: "o"}); err == nil {
		t.Fatal("a reversal of nothing should refuse")
	}
}
