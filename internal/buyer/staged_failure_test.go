// A staged walk through one request that falls outside the person's limits,
// from the quote through the refusal and the handover to the settlement, run
// against the real gate so the refusal is the shipped rail and not a stub.
package buyer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/gate"
)

// The staged amounts. The shelf price is inside the limit, the bundled ask is
// not, and the allowance covers either, so the only thing standing between the
// buyer and the purchase is the person's own ceiling.
const (
	stagedShelfPaise  = 199900
	stagedBundlePaise = 249900
	stagedLimitPaise  = 200000
	stagedWalletPaise = 500000
)

// stagedShelf answers with the one product the staged request is about.
type stagedShelf struct{}

// Get returns the staged product.
func (stagedShelf) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "trimmer", Name: "BladeMaster Pro 9", PricePaise: stagedShelfPaise, Stock: 4}, nil
}

// stagedPerson is one account with a ceiling below the bundled ask.
type stagedPerson struct{}

// AccountForTelegram returns the staged account.
func (stagedPerson) AccountForTelegram(context.Context, int64) (Account, error) {
	return Account{ID: "account", WalletBalancePaise: stagedWalletPaise, SpendLimitPaise: stagedLimitPaise}, nil
}

// stagedApprovals holds the handover and replays exactly what was authorised.
// It records what the person was asked and hands those same amounts back on
// resolution, so the resumed purchase cannot quietly settle a different figure.
type stagedApprovals struct {
	asked    ApprovalRequest
	decision string
}

// Create records the pending approval.
func (s *stagedApprovals) Create(_ context.Context, request ApprovalRequest) (ApprovalResult, error) {
	s.asked = request
	return ApprovalResult{Approved: true, Token: request.Token}, nil
}

// Resolve replays the recorded request under the person's decision.
func (s *stagedApprovals) Resolve(_ context.Context, _ int64, token string, decision string) (ApprovalResolution, error) {
	if token != s.asked.Token {
		return ApprovalResolution{Reason: "unknown approval"}, nil
	}
	s.decision = decision
	return ApprovalResolution{
		Resolved:         true,
		Approved:         decision == "approve",
		ProductID:        s.asked.ProductID,
		Quantity:         s.asked.Quantity,
		BaseAmountPaise:  s.asked.BaseAmountPaise,
		FinalAmountPaise: s.asked.FinalAmountPaise,
		IdempotencyKey:   s.asked.IdempotencyKey,
	}, nil
}

// stagedAuditor keeps every gate decision so the trail can be read back.
type stagedAuditor struct{ reasons []string }

// RecordGateDecision appends one decision reason.
func (a *stagedAuditor) RecordGateDecision(_ context.Context, decision gate.Decision) error {
	a.reasons = append(a.reasons, decision.Reason)
	return nil
}

// stagedWalk plays the sequence once and returns one line per step. The lines
// carry no clock and no generated identifier, so two runs of the same decision
// are comparable as text.
func stagedWalk(t *testing.T, decision string) []string {
	t.Helper()

	shelf := stagedShelf{}
	person := stagedPerson{}
	approvals := &stagedApprovals{}
	artifacts := &fakeArtifacts{}
	allowance := &fakeWallet{}
	auditor := &stagedAuditor{}

	moneyGate, err := gate.New(auditor, time.Hour)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	service := NewPurchaseService(shelf, person, moneyGate, artifacts, allowance, approvals)

	ask := PurchaseRequest{
		TelegramID:       42,
		ProductID:        "trimmer",
		Quantity:         1,
		BaseAmountPaise:  stagedShelfPaise,
		FinalAmountPaise: stagedBundlePaise,
		IdempotencyKey:   "staged-walk",
	}

	var steps []string
	steps = append(steps, fmt.Sprintf("quoted INR %.2f against a standing limit of INR %.2f",
		float64(ask.FinalAmountPaise)/100, float64(stagedLimitPaise)/100))

	refused, err := service.Purchase(t.Context(), ask)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if refused.Fulfilled {
		t.Fatalf("an ask above the limit settled without anyone being asked")
	}
	if !refused.ApprovalRequired || refused.ApprovalToken == "" {
		t.Fatalf("the refusal did not hand the decision over: %+v", refused)
	}
	steps = append(steps, "gate refused and gave its reason: "+refused.Reason)

	if artifacts.calls != 0 || allowance.calls != 0 {
		t.Fatalf("money moved on a refusal: %d artifacts, %d movements", artifacts.calls, allowance.calls)
	}
	steps = append(steps, "nothing spent while the answer is outstanding")

	if approvals.asked.FinalAmountPaise != ask.FinalAmountPaise {
		t.Fatalf("the person was asked about INR %.2f rather than the quoted INR %.2f",
			float64(approvals.asked.FinalAmountPaise)/100, float64(ask.FinalAmountPaise)/100)
	}
	steps = append(steps, "handed over for a decision, on the amount that was quoted")

	settled, err := service.ResolveApproval(t.Context(), ask.TelegramID, refused.ApprovalToken, decision)
	if err != nil {
		t.Fatalf("resolving the handover: %v", err)
	}
	steps = append(steps, "the person answered: "+decision)

	if decision != "approve" {
		if settled.Fulfilled || allowance.calls != 0 {
			t.Fatalf("a declined handover still spent: %+v", settled)
		}
		steps = append(steps, "declined, so nothing was spent")
		return steps
	}

	if !settled.Fulfilled {
		t.Fatalf("an approved handover did not settle: %+v", settled)
	}
	if settled.AmountPaise != ask.FinalAmountPaise {
		t.Fatalf("settled INR %.2f rather than the approved INR %.2f",
			float64(settled.AmountPaise)/100, float64(ask.FinalAmountPaise)/100)
	}
	if allowance.calls != 1 {
		t.Fatalf("expected one movement, saw %d", allowance.calls)
	}
	steps = append(steps, fmt.Sprintf("settled INR %.2f in one movement", float64(settled.AmountPaise)/100))
	steps = append(steps, "gate decisions recorded: "+strings.Join(auditor.reasons, " then "))
	return steps
}

// TestAnAskAboveTheLimitIsRefusedThenSettledOnlyAfterApproval walks the staged
// sequence and pins every step of it.
func TestAnAskAboveTheLimitIsRefusedThenSettledOnlyAfterApproval(t *testing.T) {
	steps := stagedWalk(t, "approve")
	want := []string{
		"quoted INR 2499.00 against a standing limit of INR 2000.00",
		"gate refused and gave its reason: human_approval_required",
		"nothing spent while the answer is outstanding",
		"handed over for a decision, on the amount that was quoted",
		"the person answered: approve",
		"settled INR 2499.00 in one movement",
		"gate decisions recorded: human_approval_required then approved",
	}
	if len(steps) != len(want) {
		t.Fatalf("expected %d steps, got %d: %v", len(want), len(steps), steps)
	}
	for index := range want {
		if steps[index] != want[index] {
			t.Errorf("step %d\n got: %s\nwant: %s", index+1, steps[index], want[index])
		}
	}
}

// TestTheStagedSequenceRunsTheSameWayTwice guards the demonstration itself. A
// sequence that reads differently on the second run is not something to show.
func TestTheStagedSequenceRunsTheSameWayTwice(t *testing.T) {
	first := strings.Join(stagedWalk(t, "approve"), "\n")
	second := strings.Join(stagedWalk(t, "approve"), "\n")
	if first != second {
		t.Fatalf("the sequence differed between runs\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestTheSameHandoverDeclinedSpendsNothing proves the other button is real: the
// same refusal and the same token, answered the other way, moves no money.
func TestTheSameHandoverDeclinedSpendsNothing(t *testing.T) {
	steps := stagedWalk(t, "reject")
	last := steps[len(steps)-1]
	if last != "declined, so nothing was spent" {
		t.Fatalf("declining did not end the sequence cleanly: %s", last)
	}
}
