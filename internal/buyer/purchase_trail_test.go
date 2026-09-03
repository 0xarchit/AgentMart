// Proves the purchase boundary records its refusals. Every money path here used to
// be able to return with no trail row at all, including the amount integrity check,
// which is the strongest refusal in the buyer and happens before the gate is
// consulted.
package buyer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/gate"
)

type recordedFailure struct {
	telegramID int64
	productID  string
	quantity   int
	cause      string
}

type failureTrail struct {
	rows []recordedFailure
	err  error
}

func (f *failureTrail) RecordPurchaseFailure(_ context.Context, telegramID int64, productID string, quantity int, cause error) error {
	f.rows = append(f.rows, recordedFailure{telegramID: telegramID, productID: productID, quantity: quantity, cause: cause.Error()})
	return f.err
}

// missingShelf is a catalog that cannot answer, which is one of the paths that
// returned silently.
type missingShelf struct{}

func (missingShelf) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{}, errors.New("product lookup failed")
}

func trailedService(t *testing.T, shelf catalogReader, trail *failureTrail) *PurchaseService {
	t.Helper()
	moneyGate, err := gate.New(&stagedAuditor{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewPurchaseService(shelf, &stagedPerson{}, moneyGate, &fakeArtifacts{}, &fakeWallet{}, &stagedApprovals{})
	service.UseFailureTrail(trail)
	return service
}

func TestAnAmountThatDoesNotMatchTheCatalogIsRecordedNotJustRefused(t *testing.T) {
	trail := &failureTrail{}
	service := trailedService(t, &stagedShelf{}, trail)

	// The base amount disagrees with the live catalog row, which is the refusal
	// that matters most and the one that never left a record.
	_, err := service.Purchase(t.Context(), PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         1,
		BaseAmountPaise:  stagedShelfPaise + 50_000,
		FinalAmountPaise: stagedShelfPaise + 50_000,
		IdempotencyKey:   "mismatch",
	})
	if err == nil {
		t.Fatal("an amount that does not match the catalog must be refused")
	}
	if len(trail.rows) != 1 {
		t.Fatalf("trail rows = %d, want the refusal recorded", len(trail.rows))
	}
	row := trail.rows[0]
	if row.telegramID != 42 || row.productID != "trimmer" || row.quantity != 1 {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(row.cause, "invalid") {
		t.Fatalf("cause = %q, want the reason it was refused", row.cause)
	}
}

func TestACatalogThatCannotAnswerIsRecorded(t *testing.T) {
	trail := &failureTrail{}
	service := trailedService(t, missingShelf{}, trail)

	if _, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 42, ProductID: "gone", Quantity: 1, IdempotencyKey: "missing"}); err == nil {
		t.Fatal("a failed lookup must be refused")
	}
	if len(trail.rows) != 1 || !strings.Contains(trail.rows[0].cause, "lookup failed") {
		t.Fatalf("rows = %+v, want the lookup failure recorded", trail.rows)
	}
}

func TestASettledPurchaseRecordsNoFailure(t *testing.T) {
	trail := &failureTrail{}
	service := trailedService(t, &stagedShelf{}, trail)

	result, err := service.Purchase(t.Context(), PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         1,
		BaseAmountPaise:  stagedShelfPaise,
		FinalAmountPaise: stagedShelfPaise,
		IdempotencyKey:   "settled",
	})
	if err != nil || !result.Fulfilled {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(trail.rows) != 0 {
		t.Fatalf("rows = %+v, want none for a purchase that worked", trail.rows)
	}
}

func TestAGateRefusalIsLeftToTheGateToRecord(t *testing.T) {
	trail := &failureTrail{}
	service := trailedService(t, &stagedShelf{}, trail)

	// Above the standing limit: the gate refuses and records its own decision, so
	// this boundary must not write a second row for the same event.
	result, err := service.Purchase(t.Context(), PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         2,
		BaseAmountPaise:  stagedShelfPaise * 2,
		FinalAmountPaise: stagedShelfPaise * 2,
		IdempotencyKey:   "over-limit",
	})
	if err != nil {
		t.Fatalf("a gate refusal is an outcome, not an error: %v", err)
	}
	if result.Fulfilled {
		t.Fatal("an amount above the limit was settled")
	}
	if len(trail.rows) != 0 {
		t.Fatalf("rows = %+v, want the gate to own its own refusal", trail.rows)
	}
}

func TestAFailureToRecordIsNotAllowedToHideTheFailure(t *testing.T) {
	trail := &failureTrail{err: errors.New("trail unavailable")}
	service := trailedService(t, missingShelf{}, trail)

	_, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 42, ProductID: "gone", Quantity: 1, IdempotencyKey: "missing"})
	if err == nil {
		t.Fatal("the original refusal must survive")
	}
	if !strings.Contains(err.Error(), "lookup failed") || !strings.Contains(err.Error(), "trail unavailable") {
		t.Fatalf("error = %v, want both the refusal and the recording failure", err)
	}
}

// The buyer agent can ask for a human decision without the gate having refused
// anything. That entry point had its own set of refusals and recorded none of them.
func TestAnApprovalRequestThatCannotBeBuiltIsRecorded(t *testing.T) {
	trail := &failureTrail{}
	service := trailedService(t, missingShelf{}, trail)

	_, err := service.RequestApproval(t.Context(), PurchaseRequest{TelegramID: 42, ProductID: "gone", Quantity: 1, IdempotencyKey: "ask"}, "the agent wants a person to decide")
	if err == nil {
		t.Fatal("an approval request over a catalog that cannot answer must be refused")
	}
	if len(trail.rows) != 1 || !strings.Contains(trail.rows[0].cause, "lookup failed") {
		t.Fatalf("rows = %+v, want the refusal recorded", trail.rows)
	}
}

// Resolving a handover resumes the original purchase, and the purchase records its
// own refusals. One refusal is one row: resolving must not write a second for the
// same event.
func TestAResumedPurchaseIsRecordedOnceNotTwice(t *testing.T) {
	trail := &failureTrail{}
	moneyGate, err := gate.New(&stagedAuditor{}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The authorised amount no longer agrees with the catalog, so the resumed
	// purchase refuses on amount integrity rather than settling.
	approvals := &stagedApprovals{asked: ApprovalRequest{
		Token: "token", ProductID: "trimmer", Quantity: 1,
		BaseAmountPaise: stagedShelfPaise + 50_000, FinalAmountPaise: stagedShelfPaise + 50_000,
		IdempotencyKey: "resumed",
	}}
	service := NewPurchaseService(stagedShelf{}, stagedPerson{}, moneyGate, &fakeArtifacts{}, &fakeWallet{}, approvals)
	service.UseFailureTrail(trail)

	if _, err := service.ResolveApproval(t.Context(), 42, "token", "approve"); err == nil {
		t.Fatal("an authorised amount that no longer matches the catalog must be refused")
	}
	if len(trail.rows) != 1 {
		t.Fatalf("trail rows = %d, want exactly one row for one refusal: %+v", len(trail.rows), trail.rows)
	}
}
