// Proves the reversal draws funding payments down in order, takes only what each
// still has, and states a shortfall rather than reporting success it did not get.
package buyer

import (
	"context"
	"fmt"
	"testing"

	"agentmart/internal/razorpay"
	"agentmart/internal/runid"
)

type fakeFunding struct {
	payments []FundingPayment
	err      error
}

func (f *fakeFunding) FundingPayments(context.Context, string) ([]FundingPayment, error) {
	return f.payments, f.err
}

type reversalCall struct {
	paymentID   string
	amountPaise int64
	key         string
	notes       map[string]string
}

type fakeReverser struct {
	payments map[string]razorpay.CapturedPayment
	calls    []reversalCall
	failOn   string
}

func (f *fakeReverser) Payment(_ context.Context, paymentID string) (razorpay.CapturedPayment, error) {
	payment, ok := f.payments[paymentID]
	if !ok {
		return razorpay.CapturedPayment{}, fmt.Errorf("no such payment %q", paymentID)
	}
	return payment, nil
}

func (f *fakeReverser) CreateRefund(_ context.Context, paymentID string, amountPaise int64, key string, notes map[string]string) (razorpay.Refund, error) {
	if paymentID == f.failOn {
		return razorpay.Refund{}, fmt.Errorf("gateway refused")
	}
	f.calls = append(f.calls, reversalCall{paymentID: paymentID, amountPaise: amountPaise, key: key, notes: notes})
	return razorpay.Refund{ID: "rfnd_" + paymentID, PaymentID: paymentID, Amount: amountPaise, Status: "processed"}, nil
}

func captured(amount, refunded int64) razorpay.CapturedPayment {
	return razorpay.CapturedPayment{Amount: amount, AmountRefunded: refunded, Status: "captured"}
}

func TestOneFundingPaymentCoversAnOrdinaryCancellation(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 1000000}}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{"pay_1": captured(1000000, 0)}}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 45000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReversedPaise != 45000 || result.ShortfallPaise != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.RefundIDs) != 1 || result.RefundIDs[0] != "rfnd_pay_1" {
		t.Fatalf("refund ids = %v", result.RefundIDs)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].amountPaise != 45000 {
		t.Fatalf("calls = %+v", gateway.calls)
	}
}

func TestAReversalSplitsAcrossFundingPaymentsOldestFirst(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{
		{PaymentID: "pay_old", AmountPaise: 100000},
		{PaymentID: "pay_new", AmountPaise: 500000},
	}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{
		"pay_old": captured(100000, 70000),
		"pay_new": captured(500000, 0),
	}}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 80000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReversedPaise != 80000 || result.ShortfallPaise != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(gateway.calls) != 2 {
		t.Fatalf("calls = %+v, want the older payment drawn down first then the rest", gateway.calls)
	}
	if gateway.calls[0].paymentID != "pay_old" || gateway.calls[0].amountPaise != 30000 {
		t.Fatalf("first call = %+v, want 30000 off the older payment", gateway.calls[0])
	}
	if gateway.calls[1].paymentID != "pay_new" || gateway.calls[1].amountPaise != 50000 {
		t.Fatalf("second call = %+v, want the remaining 50000", gateway.calls[1])
	}
}

func TestAnExhaustedFundingPaymentIsSkippedRatherThanAsked(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{
		{PaymentID: "pay_spent", AmountPaise: 100000},
		{PaymentID: "pay_good", AmountPaise: 100000},
	}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{
		"pay_spent": captured(100000, 100000),
		"pay_good":  captured(100000, 0),
	}}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 20000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].paymentID != "pay_good" {
		t.Fatalf("calls = %+v, want only the payment with room left", gateway.calls)
	}
	if result.ReversedPaise != 20000 {
		t.Fatalf("reversed %d", result.ReversedPaise)
	}
}

func TestNoGatewayCapacityIsReportedAsAShortfallNotAsSuccess(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 100000}}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{"pay_1": captured(100000, 100000)}}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 45000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReversedPaise != 0 || result.ShortfallPaise != 45000 {
		t.Fatalf("result = %+v, want the whole amount reported as unreversed", result)
	}
	if len(result.RefundIDs) != 0 {
		t.Fatalf("refund ids = %v, want none", result.RefundIDs)
	}
}

func TestTheReversalCarriesTheOrderAndTheConversation(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 1000000}}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{"pay_1": captured(1000000, 0)}}
	ctx := runid.With(t.Context(), "run-9")

	if _, err := NewGatewayReversal(funding, gateway).Reverse(ctx, ReverseRequest{AccountID: "account", OrderID: "order-7", AmountPaise: 45000, IdempotencyKey: "key-one-two", Reason: "Cancelled by user"}); err != nil {
		t.Fatal(err)
	}
	notes := gateway.calls[0].notes
	if notes["order_id"] != "order-7" || notes["run_id"] != "run-9" || notes["reason"] != "Cancelled by user" {
		t.Fatalf("notes = %v", notes)
	}
}

func TestAPartialFailureKeepsWhatItAlreadyReversed(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{
		{PaymentID: "pay_one", AmountPaise: 30000},
		{PaymentID: "pay_two", AmountPaise: 500000},
	}}
	gateway := &fakeReverser{
		payments: map[string]razorpay.CapturedPayment{
			"pay_one": captured(30000, 0),
			"pay_two": captured(500000, 0),
		},
		failOn: "pay_two",
	}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 80000, IdempotencyKey: "key-one-two"})
	if err == nil {
		t.Fatal("a refused reversal must surface")
	}
	if result.ReversedPaise != 30000 || len(result.RefundIDs) != 1 {
		t.Fatalf("result = %+v, want the first reversal still reported", result)
	}
}

func TestAReversalRefusesWithoutWhatItNeeds(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 100}}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{"pay_1": captured(100, 0)}}
	reversal := NewGatewayReversal(funding, gateway)

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
