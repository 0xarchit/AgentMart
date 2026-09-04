// Tests for the negotiation JSON contract.
package negotiation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentmart/internal/catalog"
)

func TestNegotiationUsesCatalogReader(t *testing.T) {
	calls := 0
	handler := NewCatalogServer(func(_ context.Context, id string) (catalog.Product, error) {
		calls++
		return catalog.Product{ID: id, PricePaise: 100, Stock: 10}, nil
	}).Handler()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"propose","product_id":"product","qty":1}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, catalog calls = %d", response.Code, calls)
	}
}

func TestNegotiationHTTPFlow(t *testing.T) {
	handler := NewServer([]catalog.Product{{ID: "product", PricePaise: 100, Stock: 10, WarrantyYears: 1}}).Handler()

	propose := httptest.NewRecorder()
	proposeRequest := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"propose","product_id":"product","qty":1}`))
	handler.ServeHTTP(propose, proposeRequest)
	if propose.Code != http.StatusOK {
		t.Fatalf("propose status = %d", propose.Code)
	}

	var counter struct {
		SessionID        string `json:"session_id"`
		BaseAmountPaise  int64  `json:"base_amount_paise"`
		FinalAmountPaise int64  `json:"final_amount_paise"`
		WarrantyYears    int    `json:"warranty_years"`
		TrustScore       int    `json:"trust_score"`
	}
	if err := json.NewDecoder(propose.Body).Decode(&counter); err != nil {
		t.Fatalf("decode counter: %v", err)
	}
	// This server has no gateway and no selling rate to read, so nothing is
	// observed and the ask is exactly the list total. Cover is only billed for when
	// the shop can see what it pays out.
	if counter.SessionID == "" || counter.BaseAmountPaise != 100 || counter.FinalAmountPaise != 100 || counter.WarrantyYears != 1 {
		t.Fatalf("unexpected counter: %+v", counter)
	}

	accept := httptest.NewRecorder()
	acceptRequest := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"accept","session_id":"`+counter.SessionID+`"}`))
	handler.ServeHTTP(accept, acceptRequest)
	if accept.Code != http.StatusOK {
		t.Fatalf("accept status = %d", accept.Code)
	}

	var result struct {
		Status           Status `json:"status"`
		BaseAmountPaise  int64  `json:"base_amount_paise"`
		FinalAmountPaise int64  `json:"final_amount_paise"`
		UpliftPaise      int64  `json:"uplift_paise"`
	}
	if err := json.NewDecoder(accept.Body).Decode(&result); err != nil {
		t.Fatalf("decode accepted session: %v", err)
	}
	if result.Status != StatusAccepted || result.BaseAmountPaise != 100 || result.FinalAmountPaise != 100 || result.UpliftPaise != 0 {
		t.Fatalf("unexpected accepted session: %+v", result)
	}
}

// TestAFundedDiscountIsAShareOfEverythingBeingBought pins the base the entitled
// floor is measured against. A campaign funds a percentage off what the buyer is
// asked to pay, and on a combo the ask includes the partner product. Measuring
// the discount against the main product alone let the shop concede far past what
// the campaign funded, all the way down to blended cost, on exactly the deals
// where it had the most to give away.
func TestAFundedDiscountIsAShareOfEverythingBeingBought(t *testing.T) {
	partnerID := "cream-1"
	stock := map[string]catalog.Product{
		// Trust sits at the threshold and nothing else is observed, so the opening
		// ask carries no uplift and the arithmetic below is only list and bundle.
		"trim-9":  {ID: "trim-9", Name: "TrimPro", PricePaise: 100_000, Stock: 5, TrustScore: 80, ComboWith: &partnerID, ComboDiscountPct: 50},
		"cream-1": {ID: "cream-1", Name: "CalmSkin", PricePaise: 40_000, Stock: 5, TrustScore: 80},
	}
	costs := map[string]int64{"trim-9": 10_000, "cream-1": 4_000}
	getProduct := func(_ context.Context, id string) (catalog.Product, error) {
		product, ok := stock[id]
		if !ok {
			return catalog.Product{}, errProductNotFound
		}
		return product, nil
	}
	getPriced := func(ctx context.Context, id string) (catalog.Product, int64, error) {
		product, err := getProduct(ctx, id)
		return product, costs[id], err
	}

	store := NewMemorySessionStore()
	server, err := NewOrchestratedServer(getProduct, getPriced, store)
	if err != nil {
		t.Fatal(err)
	}
	server.WithEntitlement(func(context.Context, string) (int, error) { return 20, nil })
	handler := server.Handler()

	ask := func(t *testing.T, body string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	opened := ask(t, `{"type":"propose","product_id":"trim-9","qty":1,"account_id":"account-1"}`)
	sessionID, _ := opened["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("no session came back: %v", opened)
	}
	// The trimmer at 100000 plus the cream at half of 40000.
	if opened["final_amount_paise"] != float64(120_000) {
		t.Fatalf("opening ask = %v, want the list total and the half price partner", opened["final_amount_paise"])
	}

	ask(t, `{"type":"counter","session_id":"`+sessionID+`","account_id":"account-1","counter_amount_paise":85000}`)

	session, ok, err := store.Get(t.Context(), sessionID)
	if err != nil || !ok {
		t.Fatalf("session missing after the counter: %v", err)
	}
	// 20% of the 120000 the buyer is actually asked for is 24000. Off the trimmer
	// alone the floor would sit at 80000, which funds a 40000 concession from a
	// campaign that pays for 24000. Blended cost is 12000 here, so nothing else is
	// holding the line.
	if session.FloorPaise != 96_000 {
		t.Fatalf("floor = %d, want 96000: the funded share of everything being bought, not of the trimmer alone", session.FloorPaise)
	}
	if session.BundledPaise != 20_000 {
		t.Fatalf("bundled = %d, want the half price partner carried on the session", session.BundledPaise)
	}
}

// TestResolvingASessionReportsWhenItWasQuoted pins the field the buyer's gate
// needs to date a stored quote. /negotiate and /accept are separate messages with
// nothing between them, so the buyer has no memory of when the price arrived and
// the shop is the only place the time exists.
func TestResolvingASessionReportsWhenItWasQuoted(t *testing.T) {
	handler := NewCatalogServer(func(_ context.Context, id string) (catalog.Product, error) {
		return catalog.Product{ID: id, Name: "Trimmer", PricePaise: 100_000, Stock: 10, TrustScore: 80}, nil
	}).Handler()
	call := func(t *testing.T, body string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	before := time.Now().UTC().Add(-time.Second)
	opened := call(t, `{"type":"propose","product_id":"trim-9","qty":1}`)
	sessionID, _ := opened["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("no session came back: %v", opened)
	}
	settled := call(t, `{"type":"accept","session_id":"`+sessionID+`"}`)
	quoted, ok := settled["quoted_at"].(string)
	if !ok || quoted == "" {
		t.Fatalf("resolve reported no quote time: %v. The buyer cannot age a price it is never told the age of.", settled)
	}
	at, err := time.Parse(time.RFC3339Nano, quoted)
	if err != nil {
		t.Fatalf("quoted_at = %q: %v", quoted, err)
	}
	if at.Before(before) || at.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("quoted_at = %v, want the moment this session was quoted", at)
	}
}
