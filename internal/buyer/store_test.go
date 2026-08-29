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

func TestTrailRowsCarryTheRunTheyCameFrom(t *testing.T) {
	// Correlation is the whole point of the trail: rows that cannot be joined
	// back to the run that caused them explain nothing.
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
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
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
