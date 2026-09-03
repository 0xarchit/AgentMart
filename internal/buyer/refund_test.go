// Tests for Telegram wallet refund orchestration.
package buyer

import (
	"context"
	"strings"
	"testing"

	"agentmart/internal/wallet"
)

type fakeRefundAccounts struct{}

func (fakeRefundAccounts) AccountForTelegram(context.Context, int64) (Account, error) {
	return Account{ID: "account"}, nil
}

type fakeRefunder struct{ request wallet.RefundRequest }

func (f *fakeRefunder) Refund(_ context.Context, request wallet.RefundRequest) (wallet.RefundResult, error) {
	f.request = request
	return wallet.RefundResult{Approved: true, OrderID: request.OrderID, AmountPaise: 1250}, nil
}

func TestRefundDerivesStableIdempotencyKey(t *testing.T) {
	refunder := &fakeRefunder{}
	service := NewRefundService(fakeRefundAccounts{}, refunder)
	result, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order", Reason: "changed mind"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved || result.AmountPaise != 1250 {
		t.Fatalf("result = %+v", result)
	}
	if refunder.request.IdempotencyKey != "telegram:7:refund:9" {
		t.Fatalf("idempotency key = %q", refunder.request.IdempotencyKey)
	}
}

func TestRefundRequiresReason(t *testing.T) {
	service := NewRefundService(fakeRefundAccounts{}, &fakeRefunder{})
	if _, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order"}); err == nil {
		t.Fatal("expected validation error")
	}
}

// idempotentRefunder credits an order once per idempotency key, the way the
// database function does, so a repeated request is answered rather than paid.
type idempotentRefunder struct {
	seen    map[string]bool
	keys    []string
	credits int
}

func (f *idempotentRefunder) Refund(_ context.Context, request wallet.RefundRequest) (wallet.RefundResult, error) {
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	f.keys = append(f.keys, request.IdempotencyKey)
	if f.seen[request.IdempotencyKey] {
		return wallet.RefundResult{Duplicate: true, OrderID: request.OrderID}, nil
	}
	f.seen[request.IdempotencyKey] = true
	f.credits++
	return wallet.RefundResult{Approved: true, OrderID: request.OrderID, AmountPaise: 1250}, nil
}

// TestASecondTapRefundsOnceAndReportsItAlreadyApplied covers the recovery path a
// person reaches by tapping the cancel button twice. The second tap carries the
// same message id, so it derives the same idempotency key, and the money layer
// answers instead of paying again.
func TestASecondTapRefundsOnceAndReportsItAlreadyApplied(t *testing.T) {
	refunder := &idempotentRefunder{}
	service := NewRefundService(fakeRefundAccounts{}, refunder)
	tap := RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order", Reason: "Cancelled by user"}

	first, err := service.Refund(t.Context(), tap)
	if err != nil {
		t.Fatalf("first tap: %v", err)
	}
	if !first.Approved || first.Duplicate || first.AmountPaise != 1250 {
		t.Fatalf("first tap = %+v, want one approved credit", first)
	}

	second, err := service.Refund(t.Context(), tap)
	if err != nil {
		t.Fatalf("second tap must be answered, not failed: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("second tap = %+v, want it reported as already applied", second)
	}
	if second.Approved || second.AmountPaise != 0 {
		t.Fatalf("second tap = %+v, want no second credit", second)
	}

	if refunder.credits != 1 {
		t.Fatalf("applied %d credits, want exactly 1", refunder.credits)
	}
	if len(refunder.keys) != 2 || refunder.keys[0] != refunder.keys[1] {
		t.Fatalf("keys = %v, want the same key twice so the guard can fire", refunder.keys)
	}
}

// refundTrail records what the refund path reports. A refusal that leaves no row
// shows up here as a missing one.
type refundTrail struct{ refusals []string }

func (r *refundTrail) RecordReversal(context.Context, string, string, ReverseResult) error {
	return nil
}

func (r *refundTrail) RecordReversalFailure(context.Context, string, string, error) error {
	return nil
}

func (r *refundTrail) RecordRefundFailure(_ context.Context, _ int64, orderID string, cause error) error {
	r.refusals = append(r.refusals, orderID+": "+cause.Error())
	return nil
}

// A refused refund is money the person was promised and did not get, so it has to
// leave a row even though it never reached the credit.
func TestARefundRefusedBeforeTheCreditIsRecorded(t *testing.T) {
	trail := &refundTrail{}
	service := NewRefundService(fakeRefundAccounts{}, &fakeRefunder{})
	service.UseReversal(nil, trail)

	if _, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order"}); err == nil {
		t.Fatal("a refund with no reason must be refused")
	}
	if len(trail.refusals) != 1 || !strings.Contains(trail.refusals[0], "order") {
		t.Fatalf("refusals = %v, want the refusal recorded against the order asked about", trail.refusals)
	}
}
