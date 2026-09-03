// Proves the sales read counts only money the gateway confirms, and that an
// unpaid artifact falls out on its state rather than on its identifier.
package razorpay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func salesServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/payments":
			_, _ = w.Write([]byte(`{"items":[
				{"id":"one","amount":1000000,"status":"captured"},
				{"id":"two","amount":24000,"status":"created"},
				{"id":"three","amount":50000,"status":"authorized"},
				{"id":"four","amount":200000,"status":"captured"}]}`))
		case "/refunds":
			_, _ = w.Write([]byte(`{"items":[
				{"id":"five","amount":45000,"status":"processed"},
				{"id":"six","amount":99000,"status":"pending"}]}`))
		case "/settlements":
			_, _ = w.Write([]byte(`{"items":[
				{"id":"seven","amount":800000,"status":"processed"},
				{"id":"eight","amount":700000,"status":"created"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSalesFactsCountOnlyWhatTheGatewayConfirms(t *testing.T) {
	var seen []string
	server := salesServer(t, &seen)
	defer server.Close()
	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	facts, err := client.SalesFacts(t.Context(), time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if facts.CapturedCount != 2 || facts.CapturedPaise != 1_200_000 {
		t.Fatalf("captured %d payments worth %d", facts.CapturedCount, facts.CapturedPaise)
	}
	if facts.IgnoredCount != 2 {
		t.Fatalf("ignored = %d, an unpaid or uncaptured payment is not revenue", facts.IgnoredCount)
	}
	if facts.RefundedCount != 1 || facts.RefundedPaise != 45_000 {
		t.Fatalf("refunded %d worth %d", facts.RefundedCount, facts.RefundedPaise)
	}
	if facts.SettledPaise != 800_000 {
		t.Fatalf("settled = %d, only a processed settlement has paid out", facts.SettledPaise)
	}
	// Three percent, not fifty. One refund against two captured payments is not a
	// shop where half of everything comes back: 45,000 paise went back out of
	// 1,200,000 taken.
	if facts.AverageCapture != 600_000 || facts.RefundRatePct != 3 {
		t.Fatalf("average %d at refund rate %d", facts.AverageCapture, facts.RefundRatePct)
	}
}

func TestTheSalesReadOnlyEverIssuesReads(t *testing.T) {
	var seen []string
	server := salesServer(t, &seen)
	defer server.Close()
	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	if _, err := client.SalesFacts(t.Context(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("requests = %v", seen)
	}
	for _, request := range seen {
		if !strings.HasPrefix(request, http.MethodGet+" ") {
			t.Fatalf("%q is not a read", request)
		}
	}
}

func TestAnEmptyWindowDoesNotDivideByZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	facts, err := client.SalesFacts(t.Context(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if facts.AverageCapture != 0 || facts.RefundRatePct != 0 {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestAFailedReadIsReportedInsteadOfGuessed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	if _, err := client.SalesFacts(t.Context(), time.Time{}); err == nil {
		t.Fatal("a refused read should surface")
	}
}
