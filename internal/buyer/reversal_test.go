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
	refunds  map[string][]razorpay.Refund
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

// PaymentRefunds answers with whatever the gateway is holding against a payment.
// A payment nothing was ever reversed off is not an error, it is an empty answer.
func (f *fakeReverser) PaymentRefunds(_ context.Context, paymentID string) ([]razorpay.Refund, error) {
	return f.refunds[paymentID], nil
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

// landedFor is one reversal the gateway already holds against an order.
func landedFor(id, orderID string, amount int64, status string) razorpay.Refund {
	return razorpay.Refund{ID: id, Amount: amount, Status: status, Notes: map[string]string{"order_id": orderID}}
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

// The run in the notes is the one the request names, never the one the call is
// running inside. A resumed reversal has to reproduce the first attempt's body,
// and the run it is resumed from is not the run it is resumed in.
func TestTheReversalCarriesTheOrderAndTheConversation(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 1000000}}}
	gateway := &fakeReverser{payments: map[string]razorpay.CapturedPayment{"pay_1": captured(1000000, 0)}}
	ctx := runid.With(t.Context(), "run-now")

	if _, err := NewGatewayReversal(funding, gateway).Reverse(ctx, ReverseRequest{AccountID: "account", OrderID: "order-7", AmountPaise: 45000, IdempotencyKey: "key-one-two", Reason: "Cancelled by user", RunID: "run-9"}); err != nil {
		t.Fatal(err)
	}
	notes := gateway.calls[0].notes
	if notes["order_id"] != "order-7" || notes["run_id"] != "run-9" || notes["reason"] != "Cancelled by user" {
		t.Fatalf("notes = %v, want the run the request names and not the one it runs in", notes)
	}
}

// The window between the gateway accepting a leg and us writing that down is the
// one an interrupted reversal stops in. Reopening it must send nothing.
func TestAReversalTheGatewayAlreadyHoldsIsNotSentAgain(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 1000000}}}
	gateway := &fakeReverser{
		payments: map[string]razorpay.CapturedPayment{"pay_1": captured(1000000, 45000)},
		refunds:  map[string][]razorpay.Refund{"pay_1": {landedFor("rfnd_landed", "order-1", 45000, "processed")}},
	}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 45000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("calls = %+v, want nothing sent for money already back", gateway.calls)
	}
	if result.ReversedPaise != 45000 || result.ShortfallPaise != 0 {
		t.Fatalf("result = %+v, want the landed reversal reported as the whole amount", result)
	}
	if len(result.RefundIDs) != 1 || result.RefundIDs[0] != "rfnd_landed" {
		t.Fatalf("refund ids = %v, want the reversal the gateway already holds", result.RefundIDs)
	}
}

// A resumed reversal sends the leg the first attempt did not get to, and sends it
// with the same amount that attempt would have used: the payment the first leg
// exhausted is skipped, so what is left lands whole on the next one.
func TestAnInterruptedReversalSendsOnlyTheLegItNeverGotTo(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{
		{PaymentID: "pay_old", AmountPaise: 30000},
		{PaymentID: "pay_new", AmountPaise: 500000},
	}}
	gateway := &fakeReverser{
		payments: map[string]razorpay.CapturedPayment{
			"pay_old": captured(30000, 30000),
			"pay_new": captured(500000, 0),
		},
		refunds: map[string][]razorpay.Refund{"pay_old": {landedFor("rfnd_first_leg", "order-1", 30000, "processed")}},
	}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 80000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 1 {
		t.Fatalf("calls = %+v, want only the leg that never landed", gateway.calls)
	}
	if gateway.calls[0].paymentID != "pay_new" || gateway.calls[0].amountPaise != 50000 {
		t.Fatalf("call = %+v, want the 50000 the first attempt would have sent", gateway.calls[0])
	}
	if gateway.calls[0].notes["part_of_paise"] != "80000" {
		t.Fatalf("notes = %v, want the whole credited amount so the body matches the first attempt", gateway.calls[0].notes)
	}
	if result.ReversedPaise != 80000 || result.ShortfallPaise != 0 {
		t.Fatalf("result = %+v, want the landed leg and the sent leg together", result)
	}
}

// Progress is totalled across every funding payment before a single leg is sent.
// A leg that landed on a later payment than the one being sized would otherwise be
// invisible, and here that would reverse the second leg's money a second time.
func TestALandedLegOnALaterPaymentIsNotSentAgain(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{
		{PaymentID: "pay_old", AmountPaise: 30000},
		{PaymentID: "pay_new", AmountPaise: 500000},
	}}
	gateway := &fakeReverser{
		payments: map[string]razorpay.CapturedPayment{
			"pay_old": captured(30000, 30000),
			"pay_new": captured(500000, 50000),
		},
		refunds: map[string][]razorpay.Refund{
			"pay_old": {landedFor("rfnd_first_leg", "order-1", 30000, "processed")},
			"pay_new": {landedFor("rfnd_second_leg", "order-1", 50000, "processed")},
		},
	}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 80000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("calls = %+v, want nothing sent when both legs already landed", gateway.calls)
	}
	if result.ReversedPaise != 80000 || len(result.RefundIDs) != 2 {
		t.Fatalf("result = %+v, want both landed legs counted", result)
	}
}

// Only money that actually went back for this order counts as progress. A
// reversal the gateway took and then failed sent none, and a reversal for another
// order is not this one's. Counting either sends this refund short.
func TestProgressCountsOnlyMoneyThatWentBackForThisOrder(t *testing.T) {
	funding := &fakeFunding{payments: []FundingPayment{{PaymentID: "pay_1", AmountPaise: 1000000}}}
	gateway := &fakeReverser{
		payments: map[string]razorpay.CapturedPayment{"pay_1": captured(1000000, 20000)},
		refunds: map[string][]razorpay.Refund{"pay_1": {
			landedFor("rfnd_failed", "order-1", 45000, "failed"),
			landedFor("rfnd_other_order", "order-2", 20000, "processed"),
		}},
	}

	result, err := NewGatewayReversal(funding, gateway).Reverse(t.Context(), ReverseRequest{AccountID: "account", OrderID: "order-1", AmountPaise: 45000, IdempotencyKey: "key-one-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].amountPaise != 45000 {
		t.Fatalf("calls = %+v, want the whole amount still sent", gateway.calls)
	}
	if result.ReversedPaise != 45000 || len(result.RefundIDs) != 1 || result.RefundIDs[0] != "rfnd_pay_1" {
		t.Fatalf("result = %+v, want only the reversal this call made", result)
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
