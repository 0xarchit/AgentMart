// Tests for Telegram wallet refund orchestration.
package buyer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"agentmart/internal/runid"
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
// shows up here as a missing one. It also stands in for the reversal a credit is
// still owed, so a resume can be driven without a database.
type refundTrail struct {
	refusals    []string
	failures    []string
	lookups     []string
	settled     []string
	reversals   []ReverseResult
	outstanding map[string]ReversalAttempt
	lookupErr   error
}

func (r *refundTrail) OutstandingReversal(_ context.Context, _, orderID string) (ReversalAttempt, bool, error) {
	r.lookups = append(r.lookups, orderID)
	if r.lookupErr != nil {
		return ReversalAttempt{}, false, r.lookupErr
	}
	attempt, found := r.outstanding[orderID]
	return attempt, found, nil
}

func (r *refundTrail) SettleReversal(_ context.Context, orderID string) error {
	r.settled = append(r.settled, orderID)
	return nil
}

func (r *refundTrail) RecordReversal(_ context.Context, _, _ string, result ReverseResult) error {
	r.reversals = append(r.reversals, result)
	return nil
}

func (r *refundTrail) RecordReversalFailure(_ context.Context, _, orderID string, cause error) error {
	r.failures = append(r.failures, orderID+": "+cause.Error())
	return nil
}

func (r *refundTrail) RecordRefundFailure(_ context.Context, _ int64, orderID string, cause error) error {
	r.refusals = append(r.refusals, orderID+": "+cause.Error())
	return nil
}

// stubReversal stands in for the gateway side so the orchestration can be tested
// on its own. What it was asked to reverse is the whole assertion.
type stubReversal struct {
	requests []ReverseRequest
	result   ReverseResult
	err      error
}

func (s *stubReversal) Reverse(_ context.Context, request ReverseRequest) (ReverseResult, error) {
	s.requests = append(s.requests, request)
	return s.result, s.err
}

// refusingRefunder answers the way the wallet does for an order it has already
// refunded: no credit to make, no duplicate to recognise, just a reason. This is
// what a person's second /refund meets after the first one was interrupted.
type refusingRefunder struct{}

func (refusingRefunder) Refund(context.Context, wallet.RefundRequest) (wallet.RefundResult, error) {
	return wallet.RefundResult{Reason: "order is not refundable in its current state"}, nil
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

// A credit made here and now reverses itself, with the run it ran in and the key
// it was credited under, and without asking what an earlier attempt left: there
// was no earlier attempt.
func TestAFreshCreditReversesItselfWithTheRunItRanIn(t *testing.T) {
	trail := &refundTrail{}
	reversal := &stubReversal{result: ReverseResult{RefundIDs: []string{"rfnd_1"}, ReversedPaise: 1250}}
	service := NewRefundService(fakeRefundAccounts{}, &fakeRefunder{})
	service.UseReversal(reversal, trail)

	ctx := runid.With(t.Context(), "run-now")
	if _, err := service.Refund(ctx, RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order", Reason: "Cancelled by user"}); err != nil {
		t.Fatal(err)
	}
	if len(reversal.requests) != 1 {
		t.Fatalf("requests = %+v, want the credit reversed once", reversal.requests)
	}
	got := reversal.requests[0]
	if got.RunID != "run-now" || got.IdempotencyKey != "telegram:7:refund:9" || got.AmountPaise != 1250 || got.Reason != "Cancelled by user" {
		t.Fatalf("request = %+v, want this run's credit reversed as it was made", got)
	}
	if len(trail.lookups) != 0 {
		t.Fatalf("lookups = %v, want a fresh credit reversed without asking what is owed", trail.lookups)
	}
	if len(trail.settled) != 1 || trail.settled[0] != "order" {
		t.Fatalf("settled = %v, want a finished reversal marked no longer owed", trail.settled)
	}
}

// A gateway that does not answer leaves the reversal owed. This is the whole point
// of writing it down: the credit stands, the person is told, and the next attempt
// finishes it instead of starting a second one.
func TestAGatewayFailureLeavesTheReversalOwed(t *testing.T) {
	trail := &refundTrail{}
	reversal := &stubReversal{err: fmt.Errorf("gateway unreachable")}
	service := NewRefundService(fakeRefundAccounts{}, &fakeRefunder{})
	service.UseReversal(reversal, trail)

	outcome, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order", Reason: "Cancelled by user"})
	if err != nil {
		t.Fatalf("the allowance is credited, so the refund must not fail: %v", err)
	}
	if !outcome.Approved {
		t.Fatalf("outcome = %+v, want the credit still reported", outcome)
	}
	if len(trail.settled) != 0 {
		t.Fatalf("settled = %v, want the reversal still owed so it can be resumed", trail.settled)
	}
	if len(trail.failures) != 1 {
		t.Fatalf("failures = %v, want the gateway failure on the trail", trail.failures)
	}
}

// The ordinary way a resume arrives is a refusal. The order is already refunded, so
// a second /refund makes no credit and is no duplicate, and the reversal the first
// attempt could not finish is finished on the back of it.
func TestARefusedRefundFinishesAReversalAnEarlierAttemptLeft(t *testing.T) {
	trail := &refundTrail{outstanding: map[string]ReversalAttempt{"order-7": {
		OrderID:        "order-7",
		AmountPaise:    80000,
		Reason:         "Cancelled by user",
		IdempotencyKey: "telegram:7:refund:9",
		RunID:          "run-first",
	}}}
	reversal := &stubReversal{result: ReverseResult{RefundIDs: []string{"rfnd_second_leg"}, ReversedPaise: 80000}}
	service := NewRefundService(fakeRefundAccounts{}, refusingRefunder{})
	service.UseReversal(reversal, trail)

	// A later message, a later run, and the person wording it differently. None of
	// that may reach the gateway: it would make the resumed leg a new request under
	// a key that has already sent money back.
	ctx := runid.With(t.Context(), "run-second")
	outcome, err := service.Refund(ctx, RefundRequest{TelegramID: 7, MessageID: 21, OrderID: "order-7", Reason: "still want it back"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Approved || outcome.Duplicate {
		t.Fatalf("outcome = %+v, want the wallet's refusal reported as it stands", outcome)
	}
	if len(reversal.requests) != 1 {
		t.Fatalf("requests = %+v, want the interrupted reversal resumed once", reversal.requests)
	}
	got := reversal.requests[0]
	if got.OrderID != "order-7" || got.AmountPaise != 80000 || got.Reason != "Cancelled by user" {
		t.Fatalf("resumed request = %+v, want the amount and reason the first attempt used", got)
	}
	if got.IdempotencyKey != "telegram:7:refund:9" || got.RunID != "run-first" {
		t.Fatalf("resumed request = %+v, want the first attempt's key and run", got)
	}
	if len(trail.reversals) != 1 || trail.reversals[0].ReversedPaise != 80000 {
		t.Fatalf("reversals = %+v, want the finished reversal on the trail", trail.reversals)
	}
	if len(trail.settled) != 1 || trail.settled[0] != "order-7" {
		t.Fatalf("settled = %v, want the reversal marked no longer owed", trail.settled)
	}
}

// Not knowing what is owed is not an answer to give a person, and it is not a
// licence to send money either. It leaves a row and stops.
func TestAFailedResumeCheckRecordsItAndSendsNothing(t *testing.T) {
	trail := &refundTrail{lookupErr: fmt.Errorf("attempt lookup unavailable")}
	reversal := &stubReversal{}
	service := NewRefundService(fakeRefundAccounts{}, refusingRefunder{})
	service.UseReversal(reversal, trail)

	outcome, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order-7", Reason: "Cancelled by user"})
	if err != nil {
		t.Fatalf("a failed check must not become the person's answer: %v", err)
	}
	if outcome.Approved || outcome.Reason == "" {
		t.Fatalf("outcome = %+v, want the wallet's own answer unchanged", outcome)
	}
	if len(reversal.requests) != 0 {
		t.Fatalf("requests = %+v, want nothing sent while what is owed is unknown", reversal.requests)
	}
	if len(trail.failures) != 1 || !strings.Contains(trail.failures[0], "order-7") {
		t.Fatalf("failures = %v, want the failed check recorded against the order", trail.failures)
	}
	if len(trail.settled) != 0 {
		t.Fatalf("settled = %v, want nothing settled on a check that failed", trail.settled)
	}
}
