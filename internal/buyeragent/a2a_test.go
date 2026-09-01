// Tests for the buyer's public agent surface. Its central claim is that an
// outside caller can get a negotiated quote and cannot cause a charge, so that
// claim is asserted here rather than only described in the package comment.
package buyeragent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// stubShopper returns a canned shopping result.
type stubShopper struct {
	result shopgraph.Result
	err    error
	wallet shopgraph.Wallet
}

// Run records the wallet it was handed and returns the canned result.
func (s *stubShopper) Run(_ context.Context, _ string, wallet shopgraph.Wallet) (shopgraph.Result, error) {
	s.wallet = wallet
	return s.result, s.err
}

// settledQuote is a completed negotiation an outside caller might receive.
func settledQuote() shopgraph.Result {
	return shopgraph.Result{
		Action: shopgraph.ActionBuy, ProductID: "trim-9", ProductName: "BladeMaster Pro 9",
		Quantity: 1, FinalPaise: 290000, Accepted: true, SessionID: "session-1",
		Rationale:  "inside budget with the cover included",
		Transcript: []negotiation.Turn{{Actor: "buyer", Message: "a trimmer under 3000"}},
	}
}

// drive runs the executor over one text message and collects what it yielded.
func drive(t *testing.T, shopper Shopper, text string) ([]a2a.Event, error) {
	t.Helper()
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    a2a.TaskID("task-1"),
		ContextID: "context-1",
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text)),
	}
	var events []a2a.Event
	var failed error
	for event, err := range (&executor{shopper: shopper}).Execute(t.Context(), execCtx) {
		if err != nil {
			failed = err
			break
		}
		events = append(events, event)
	}
	return events, failed
}

// lastState returns the final task state the executor reported.
func lastState(events []a2a.Event) a2a.TaskState {
	var state a2a.TaskState
	for _, event := range events {
		if update, ok := event.(*a2a.TaskStatusUpdateEvent); ok {
			state = update.Status.State
		}
	}
	return state
}

// artifactText returns the text of the first artifact the executor produced.
func artifactText(events []a2a.Event) string {
	for _, event := range events {
		update, ok := event.(*a2a.TaskArtifactUpdateEvent)
		if !ok || update.Artifact == nil {
			continue
		}
		for _, part := range update.Artifact.Parts {
			if text := part.Text(); text != "" {
				return text
			}
		}
	}
	return ""
}

func TestAQuoteCarriesTermsAndEvidenceButNoCharge(t *testing.T) {
	events, err := drive(t, &stubShopper{result: settledQuote()}, `{"request":"a trimmer under 3000","budget_paise":300000}`)
	if err != nil {
		t.Fatal(err)
	}

	var response shopResponse
	if err := json.Unmarshal([]byte(artifactText(events)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.FinalAmountPaise != 290000 || response.ProductID != "trim-9" || !response.MerchantAccepted {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Transcript) == 0 {
		t.Fatal("a quote arrived without the conversation that produced it")
	}
	if !strings.Contains(response.Note, "quote only") {
		t.Fatalf("note = %q, want the quote-only limit stated to the caller", response.Note)
	}

	// The shape itself is the guarantee: there is nowhere in this response to put a
	// payment, an order or a wallet movement, so a caller cannot mistake a quote
	// for a purchase. The note is excluded because its whole job is to name the
	// things this surface will not do.
	terms := response
	terms.Note = ""
	shape, err := json.Marshal(terms)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"payment", "razorpay", "wallet", "debit", "order_id", "refund"} {
		if strings.Contains(strings.ToLower(string(shape)), forbidden) {
			t.Fatalf("a quote mentioned %q: %s", forbidden, shape)
		}
	}
}

func TestAnOutsideCallerCannotSpendMoreThanItDeclared(t *testing.T) {
	shopper := &stubShopper{result: settledQuote()}
	if _, err := drive(t, shopper, `{"request":"a trimmer","budget_paise":250000}`); err != nil {
		t.Fatal(err)
	}
	// There is no account behind an outside caller, so its own stated budget is the
	// only ceiling the graph reasons against, and it must be all three bounds.
	if shopper.wallet.BalancePaise != 250000 || shopper.wallet.SpendLimitPaise != 250000 || shopper.wallet.BudgetPaise != 250000 {
		t.Fatalf("wallet = %+v", shopper.wallet)
	}
}

func TestAQuoteNeedingSignOffIsNotReportedAsFinished(t *testing.T) {
	result := settledQuote()
	result.Action = shopgraph.ActionAskHuman
	result.NeedsApproval = true

	events, err := drive(t, &stubShopper{result: result}, "a trimmer under 3000")
	if err != nil {
		t.Fatal(err)
	}
	if state := lastState(events); state != a2a.TaskStateInputRequired {
		t.Fatalf("state = %q, want the caller told a person still has to approve", state)
	}
}

func TestPlainTextIsAValidRequest(t *testing.T) {
	events, err := drive(t, &stubShopper{result: settledQuote()}, "a trimmer under 3000")
	if err != nil {
		t.Fatal(err)
	}
	if state := lastState(events); state != a2a.TaskStateCompleted {
		t.Fatalf("state = %q", state)
	}
}

func TestAnEmptyRequestIsRejectedRatherThanShopped(t *testing.T) {
	shopper := &stubShopper{result: settledQuote()}
	events, err := drive(t, shopper, `{"request":"   "}`)
	if err != nil {
		t.Fatal(err)
	}
	if state := lastState(events); state != a2a.TaskStateRejected {
		t.Fatalf("state = %q, want rejected", state)
	}
	if shopper.wallet.BudgetPaise != 0 {
		t.Fatal("an empty request reached the shopping graph")
	}
}

func TestAFailedRunIsReportedNotHiddenBehindAnEmptyQuote(t *testing.T) {
	events, err := drive(t, &stubShopper{err: errors.New("the shop never answered")}, "a trimmer")
	if err != nil {
		t.Fatal(err)
	}
	if state := lastState(events); state != a2a.TaskStateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
	if artifactText(events) != "" {
		t.Fatal("a failed run still produced a quote")
	}
}

func TestTheCardTellsCallersItCannotMoveMoney(t *testing.T) {
	handler, err := NewHandler(&stubShopper{result: settledQuote()}, "http://buyer.test/a2a/")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("card status = %d", recorder.Code)
	}

	var card struct {
		Description string `json:"description"`
		Skills      []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if !strings.Contains(strings.ToLower(card.Description), "never moves money") {
		t.Fatalf("description = %q, want the limit published on the card", card.Description)
	}
	// No advertised skill may read as something that charges. A caller choosing a
	// skill from this card must not find one that promises to pay.
	for _, skill := range card.Skills {
		for _, forbidden := range []string{"pay", "charge", "checkout", "debit", "settle"} {
			if strings.Contains(strings.ToLower(skill.ID), forbidden) {
				t.Fatalf("skill %q advertises %q", skill.ID, forbidden)
			}
		}
	}
}

func TestAHandlerRefusesToStartWithoutItsParts(t *testing.T) {
	if _, err := NewHandler(nil, "http://buyer.test/a2a/"); err == nil {
		t.Fatal("a handler was built with nothing to shop with")
	}
	if _, err := NewHandler(&stubShopper{}, "   "); err == nil {
		t.Fatal("a handler was built with no address to publish")
	}
}
