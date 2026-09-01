// Tests for reading trading conditions. Every observation is proved to be either
// real or absent: nothing here may invent a fact that would raise an ask.
package trading

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentmart/internal/razorpay"
	"agentmart/internal/supabase"
)

// stubGateway stands in for the payment gateway's read only figures.
type stubGateway struct {
	facts razorpay.SalesFacts
	err   error
	calls int
}

// SalesFacts returns the canned figures and counts how often it was asked.
func (s *stubGateway) SalesFacts(_ context.Context, _ time.Time) (razorpay.SalesFacts, error) {
	s.calls++
	return s.facts, s.err
}

// tradingDB serves one selling rate view response.
func tradingDB(t *testing.T, body string) *supabase.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("selling rate was read with %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAProductNobodyHasBoughtHasNothingObserved(t *testing.T) {
	provider := NewProvider(tradingDB(t, `[]`), nil)

	conditions, err := provider.Conditions(t.Context(), "product")
	if err != nil {
		t.Fatal(err)
	}
	if conditions.Observed {
		t.Fatal("a missing row was reported as an observation")
	}
	if conditions.RefundRateKnown {
		t.Fatal("no gateway was configured yet the refund rate was claimed as known")
	}
}

func TestAMeasuredSellingRateIsCarriedThrough(t *testing.T) {
	provider := NewProvider(tradingDB(t, `[{"product_id":"product","stock":2,"units_sold":9,"stock_cover_days":6}]`), nil)

	conditions, err := provider.Conditions(t.Context(), "product")
	if err != nil {
		t.Fatal(err)
	}
	if !conditions.Observed || conditions.UnitsSold != 9 || conditions.StockCoverDays != 6 {
		t.Fatalf("conditions = %+v", conditions)
	}
}

func TestAGatewayThatCannotBeReadLeavesTheRateUnknown(t *testing.T) {
	provider := &Provider{db: tradingDB(t, `[{"product_id":"product","stock":2,"units_sold":9,"stock_cover_days":6}]`)}

	conditions, err := provider.Conditions(t.Context(), "product")
	if err != nil {
		t.Fatal(err)
	}
	// The selling rate still counts. Only the figure that could not be read is
	// withheld, and it is withheld rather than defaulted to something flattering.
	if !conditions.Observed {
		t.Fatal("a readable selling rate was discarded with the gateway")
	}
	if conditions.RefundRateKnown || conditions.RefundRatePct != 0 {
		t.Fatalf("conditions = %+v", conditions)
	}
}

func TestAProductMustBeNamed(t *testing.T) {
	provider := NewProvider(tradingDB(t, `[]`), nil)
	if _, err := provider.Conditions(t.Context(), "  "); err == nil {
		t.Fatal("a blank product was priced against")
	}
}

func TestAnUnconfiguredProviderSaysSoInsteadOfGuessing(t *testing.T) {
	if NewProvider(nil, nil) != nil {
		t.Fatal("a provider was built without a database")
	}
	var provider *Provider
	if _, err := provider.Conditions(t.Context(), "product"); err == nil {
		t.Fatal("an unconfigured provider answered")
	}
}

// gatewayFacts is the shape the gateway returns, kept here so the reuse test can
// assert on a figure rather than on a call count alone.
func gatewayFacts() razorpay.SalesFacts {
	return razorpay.SalesFacts{CapturedCount: 4, CapturedPaise: 400, RefundedCount: 1, RefundRatePct: 25, AverageCapture: 100}
}

func TestOneGatewayReadServesRepeatedQuotes(t *testing.T) {
	gateway := &stubGateway{facts: gatewayFacts()}
	provider := NewProvider(tradingDB(t, `[{"product_id":"product","stock":2,"units_sold":9,"stock_cover_days":6}]`), gateway)

	for range 3 {
		conditions, err := provider.Conditions(t.Context(), "product")
		if err != nil {
			t.Fatal(err)
		}
		if !conditions.RefundRateKnown || conditions.RefundRatePct != 25 {
			t.Fatalf("conditions = %+v", conditions)
		}
	}
	// Pricing happens inside a conversation, so three quotes must not cost three
	// round trips for a figure that moves over days.
	if gateway.calls != 1 {
		t.Fatalf("gateway was read %d times, want 1", gateway.calls)
	}
}

// TestTheViewIsReadNotWritten proves this package cannot change anything: the
// selling rate is a view, and the only verb used against it is a read.
func TestTheViewIsReadNotWritten(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if got := r.URL.Query().Get("product_id"); got != "eq.product" {
			t.Errorf("filter = %q", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	client, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProvider(client, nil).Conditions(t.Context(), "product"); err != nil {
		t.Fatal(err)
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatalf("selling rate was reached with %s", method)
		}
	}
}

// TestSalesFactsShapeIsWhatWeRead pins the fields this package depends on, so a
// change to the gateway reader cannot silently stop feeding a price.
func TestSalesFactsShapeIsWhatWeRead(t *testing.T) {
	encoded, err := json.Marshal(gatewayFacts())
	if err != nil {
		t.Fatal(err)
	}
	var back razorpay.SalesFacts
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back.RefundRatePct != 25 || back.AverageCapture != 100 {
		t.Fatalf("facts = %+v", back)
	}
}
