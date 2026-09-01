// Tests for the merchant's pricing trail. A price nobody can explain afterwards
// is the thing this package exists to prevent, so the facts a price was argued
// from have to reach the row, not just the amount.
package marketaudit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/marketgraph"
	"agentmart/internal/negotiation"
	"agentmart/internal/runid"
	"agentmart/internal/supabase"
)

// captured is one row as it arrived at the database.
type captured struct {
	AccountID string         `json:"account_id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Reason    string         `json:"reason"`
	RunID     string         `json:"run_id"`
	Payload   map[string]any `json:"payload"`
}

// auditServer records what the store sends and returns success.
func auditServer(t *testing.T, rows *[]captured) *supabase.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read row: %v", err)
			return
		}
		var batch []captured
		if err := json.Unmarshal(body, &batch); err != nil {
			var single captured
			if err := json.Unmarshal(body, &single); err != nil {
				t.Errorf("decode row: %v (%s)", err, body)
				return
			}
			batch = []captured{single}
		}
		*rows = append(*rows, batch...)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	client, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// pricedFacts is a decision with every explanation field populated.
func pricedFacts() (negotiation.CounterInput, marketgraph.Facts, marketgraph.Decision) {
	in := negotiation.CounterInput{BuyerAccountID: "account-1"}
	in.Product.ID = "trim-9"
	facts := marketgraph.Facts{
		AskPaise: 314920, FloorPaise: 210000, BuyerPaise: 250000, MinAcceptablePaise: 280000,
		Round: 2, MaxRounds: 3, ProductName: "BladeMaster Pro 9",
		LoyaltyTier: "gold", LoyaltyDiscountPct: 8,
		TradingObserved: true, UnitsSoldRecently: 14, StockCoverDays: 3,
		RefundRatePct: 6, RefundRateKnown: true,
	}
	decision := marketgraph.Decision{
		AmountPaise: 290000, Reason: "best price we can hold", Strategy: marketgraph.StrategyConcede,
		GuardNote: "raised to the concession floor", MarginPaise: 80000,
	}
	return in, facts, decision
}

func TestThePriceTrailCarriesWhatThePriceWasArguedFrom(t *testing.T) {
	var rows []captured
	store := New(auditServer(t, &rows))
	in, facts, decision := pricedFacts()

	if err := store.RecordOfferDecision(t.Context(), in, facts, decision); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want one", len(rows))
	}
	row := rows[0]

	if row.Actor != "merchant_agent" || row.Action != "offer_priced" {
		t.Fatalf("row = %+v", row)
	}
	// The guard's correction must be readable beside the reason, or the trail says
	// a price was chosen when it was actually clamped.
	if !strings.Contains(row.Reason, "guard: raised to the concession floor") {
		t.Fatalf("reason = %q", row.Reason)
	}
	for _, key := range []string{
		"ask_paise", "floor_paise", "buyer_paise", "min_acceptable_paise",
		"trading_observed", "units_sold_recently", "stock_cover_days",
		"refund_rate_pct", "refund_rate_known",
	} {
		if _, ok := row.Payload[key]; !ok {
			t.Fatalf("payload is missing %q, so this price cannot be explained: %v", key, row.Payload)
		}
	}
	if row.Payload["units_sold_recently"] != float64(14) {
		t.Fatalf("selling rate = %v", row.Payload["units_sold_recently"])
	}
	if row.Payload["refund_rate_known"] != true {
		t.Fatalf("refund confidence = %v", row.Payload["refund_rate_known"])
	}
}

func TestAnAnonymousCallerStillLeavesAMerchantTrail(t *testing.T) {
	var rows []captured
	store := New(auditServer(t, &rows))
	in, facts, decision := pricedFacts()
	in.BuyerAccountID = ""

	if err := store.RecordOfferDecision(t.Context(), in, facts, decision); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	// No buyer to attribute it to, but the shop's own explanation still exists.
	if rows[0].AccountID != "" {
		t.Fatalf("account = %q, want it left unset", rows[0].AccountID)
	}
	if rows[0].Action != "offer_priced" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestThePricingTrailJoinsTheRunItBelongsTo(t *testing.T) {
	var rows []captured
	store := New(auditServer(t, &rows))
	in, facts, decision := pricedFacts()

	ctx := runid.With(t.Context(), "run-77")
	if err := store.RecordOfferDecision(ctx, in, facts, decision); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RunID != "run-77" {
		t.Fatalf("rows = %+v, want the run carried through", rows)
	}
}

func TestAnUnconfiguredAuditorRefusesRatherThanDroppingTheRow(t *testing.T) {
	// The merchant graph fails closed on this error. Returning nil here would turn
	// a missing trail into a silent success and a priced offer nobody can justify.
	if New(nil) != nil {
		t.Fatal("an auditor was built without a database")
	}
	var store *Store
	in, facts, decision := pricedFacts()
	if err := store.RecordOfferDecision(t.Context(), in, facts, decision); err == nil {
		t.Fatal("an unconfigured auditor reported success")
	}
}

func TestARefusedWriteIsReportedSoTheGraphCanFailClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"trail is unavailable"}`))
	}))
	defer server.Close()
	client, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	in, facts, decision := pricedFacts()
	if err := New(client).RecordOfferDecision(t.Context(), in, facts, decision); err == nil {
		t.Fatal("a refused write was reported as a recorded price")
	}
}
