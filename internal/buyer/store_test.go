// Tests for run correlation on the trail.
package buyer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/gate"
	"agentmart/internal/negotiation"
	"agentmart/internal/runid"
	"agentmart/internal/supabase"
)

// trailWatcher returns a store whose inserts are decoded onto the channel, so a
// test can read the row the trail would actually have received.
func trailWatcher(t *testing.T) (*Store, <-chan map[string]any) {
	t.Helper()
	posted := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read insert: %v", err)
			return
		}
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Errorf("decode insert: %v", err)
			return
		}
		posted <- row
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)

	db, err := supabase.NewClient(server.URL, "test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db), posted
}

func TestTrailRowsCarryTheRunTheyCameFrom(t *testing.T) {
	// Correlation is the whole point of the trail: rows that cannot be joined
	// back to the run that caused them explain nothing.
	store, posted := trailWatcher(t)
	decision := gate.Decision{Approved: true, Reason: "within limits"}

	ctx := runid.With(t.Context(), "11111111-1111-1111-1111-111111111111")
	if err := store.RecordGateDecision(ctx, decision); err != nil {
		t.Fatalf("record inside a run: %v", err)
	}
	row := <-posted
	if row["run_id"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("row was written unattached to its run: %v", row["run_id"])
	}

	if err := store.RecordGateDecision(t.Context(), decision); err != nil {
		t.Fatalf("record outside a run: %v", err)
	}
	if _, tagged := (<-posted)["run_id"]; tagged {
		t.Fatal("work outside a run must not invent one")
	}
}

func TestTheRecordedRunKeepsWhatTheTwoSidesSaid(t *testing.T) {
	run := AgentRun{Action: "BUY", ProductID: "product", FinalPaise: 1000}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "transcript") {
		t.Fatal("an empty conversation must not pad the row")
	}

	run.Transcript = []negotiation.Turn{{Actor: "merchant", Message: "Welcome in"}}
	encoded, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "Welcome in") {
		t.Fatalf("the words behind the price were dropped: %s", encoded)
	}
}

// TestTheGateRowSaysWhichWayTheGateWent is the difference between an audit trail
// and a row count. Only the run id was ever asserted here, so swapping the two
// action strings would have recorded every refusal as an approval, and dropping
// the amount would have left a trail that cannot say what was almost spent, with
// the whole suite still green.
func TestTheGateRowSaysWhichWayTheGateWent(t *testing.T) {
	store, posted := trailWatcher(t)
	request := gate.Request{
		AccountID: "account-3", ProductID: "trim-9", Quantity: 2, FinalAmountPaise: 359800,
	}

	if err := store.RecordGateDecision(t.Context(), gate.Decision{
		Approved: true, Reason: "within limits", Request: request,
	}); err != nil {
		t.Fatal(err)
	}
	row := <-posted
	if row["action"] != "gate_approved" {
		t.Fatalf("an approval was recorded as %q", row["action"])
	}
	if row["actor"] != "gate" || row["account_id"] != "account-3" || row["reason"] != "within limits" {
		t.Fatalf("row = %v, want the gate, the account it judged and why", row)
	}
	payload, ok := row["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want the basket the gate judged", row["payload"])
	}
	if payload["product_id"] != "trim-9" {
		t.Fatalf("payload names product %v, want the one judged", payload["product_id"])
	}
	// JSON numbers decode as float64, so compare the value rather than the type.
	if payload["quantity"] != float64(2) || payload["amount_paise"] != float64(359800) {
		t.Fatalf("payload = %v, want the quantity and the amount the gate ruled on", payload)
	}

	// The same request refused has to read as a refusal. This is the assertion a
	// swapped pair of action strings fails.
	if err := store.RecordGateDecision(t.Context(), gate.Decision{
		Approved: false, Reason: "insufficient_wallet_balance", Request: request,
	}); err != nil {
		t.Fatal(err)
	}
	refused := <-posted
	if refused["action"] != "gate_rejected" {
		t.Fatalf("a refusal was recorded as %q", refused["action"])
	}
	if refused["reason"] != "insufficient_wallet_balance" {
		t.Fatalf("the refusal does not say why: %v", refused["reason"])
	}
}
