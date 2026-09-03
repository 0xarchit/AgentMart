// Proves the reversal path: where it posts, what it sends, that every leg of one
// reversal carries its own retry key, and that only a captured payment has anything
// left to give back.
package razorpay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Callers total these to work out what a reversal already sent, so a first page
// taken for the whole list undercounts the money already back, and an undercount
// there sends it a second time. A full page has to be followed.
func TestListingReversalsFollowsThePagesToTheEnd(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.URL.Query().Get("skip") == "0" {
			items := make([]string, 0, 100)
			for i := 0; i < 100; i++ {
				items = append(items, fmt.Sprintf(`{"id":"rfnd_%d","amount":100,"status":"processed"}`, i))
			}
			_, _ = w.Write([]byte(`{"count":100,"items":[` + strings.Join(items, ",") + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"rfnd_last","amount":45000,"status":"failed","notes":{"order_id":"order-1"}}]}`))
	}))
	defer server.Close()

	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	refunds, err := client.PaymentRefunds(t.Context(), "pay_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("made %d requests, want a full page followed by the next: %v", len(requests), requests)
	}
	if !strings.Contains(requests[0], "count=100") || !strings.Contains(requests[1], "skip=100") {
		t.Fatalf("requests = %v, want the page size stated and the next page asked for", requests)
	}
	if len(refunds) != 101 {
		t.Fatalf("read %d reversals, want every page kept", len(refunds))
	}
	last := refunds[100]
	if !last.Failed() || last.Notes["order_id"] != "order-1" {
		t.Fatalf("last reversal = %+v, want the status and notes progress is judged by", last)
	}
}

func TestAReversalPostsAgainstThePaymentAndCarriesARetryKey(t *testing.T) {
	var path, method, key, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method, key = r.URL.Path, r.Method, r.Header.Get("X-Refund-Idempotency")
		buffer := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buffer)
		body = string(buffer)
		_, _ = w.Write([]byte(`{"id":"reversal-1","payment_id":"pay_1","amount":45000,"status":"processed"}`))
	}))
	defer server.Close()

	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	refund, err := client.CreateRefund(t.Context(), "pay_1", 45000, "telegram:42:refund:7", map[string]string{"order_id": "order-1"})
	if err != nil {
		t.Fatal(err)
	}
	if refund.ID != "reversal-1" || refund.Amount != 45000 {
		t.Fatalf("refund = %+v", refund)
	}
	if method != http.MethodPost || path != "/payments/pay_1/refund" {
		t.Fatalf("sent %s %s", method, path)
	}
	if !strings.Contains(body, `"amount":45000`) {
		t.Fatalf("body did not carry the amount: %s", body)
	}
	if strings.Contains(key, ":") || len(key) < 10 {
		t.Fatalf("retry key %q is not in the shape the gateway accepts", key)
	}
	if key != reversalKey("telegram:42:refund:7", "pay_1", 45000) {
		t.Fatalf("the header carried %q, which is not the key derived for this leg", key)
	}
}

// A reversal can span several funding payments, and this is the difference between
// the second leg going through and the gateway refusing it as a different request
// under a key it has already seen, once the first leg's money has gone back.
func TestEveryLegOfAReversalCarriesItsOwnRetryKey(t *testing.T) {
	const internal = "telegram:42:refund:7"
	first := reversalKey(internal, "pay_1", 45000)
	second := reversalKey(internal, "pay_2", 15000)
	if first == second {
		t.Fatalf("both legs of one reversal derived the same key %q", first)
	}
	if reversalKey(internal, "pay_1", 45000) != first {
		t.Fatal("a retry of the same leg must reproduce its key to land on the same refund")
	}
	if reversalKey(internal, "pay_1", 44000) == first {
		t.Fatal("a different amount from the same payment is a different refund")
	}
	if reversalKey("telegram:42:refund:8", "pay_1", 45000) == first {
		t.Fatal("two separate reversals of the same amount must not merge into one refund")
	}
	for _, key := range []string{first, second} {
		if len(key) < 10 {
			t.Fatalf("key %q is below the ten character minimum", key)
		}
		for _, r := range key {
			allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !allowed {
				t.Fatalf("key %q contains %q, which the gateway rejects", key, r)
			}
		}
	}
	if reversalKey("   ", "pay_1", 45000) != "" {
		t.Fatal("no internal key means no deduplication was asked for, so none is invented")
	}
}

func TestOnlyACapturedPaymentHasAnythingLeftToGiveBack(t *testing.T) {
	cases := []struct {
		name    string
		payment CapturedPayment
		want    int64
	}{
		{"captured and untouched", CapturedPayment{Amount: 1000000, Status: "captured"}, 1000000},
		{"captured and partly reversed", CapturedPayment{Amount: 1000000, AmountRefunded: 250000, Status: "captured"}, 750000},
		{"captured and fully reversed", CapturedPayment{Amount: 1000000, AmountRefunded: 1000000, Status: "captured"}, 0},
		{"never captured", CapturedPayment{Amount: 1000000, Status: "created"}, 0},
		{"reversed beyond its amount", CapturedPayment{Amount: 1000, AmountRefunded: 4000, Status: "captured"}, 0},
	}
	for _, test := range cases {
		if got := test.payment.Refundable(); got != test.want {
			t.Fatalf("%s: refundable = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestReadingAPaymentReportsWhatIsAlreadyReversed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/payments/pay_9" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"pay_9","amount":1000000,"amount_refunded":45000,"status":"captured"}`))
	}))
	defer server.Close()

	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	payment, err := client.Payment(t.Context(), "pay_9")
	if err != nil {
		t.Fatal(err)
	}
	if payment.AmountRefunded != 45000 || payment.Refundable() != 955000 {
		t.Fatalf("payment = %+v, refundable = %d", payment, payment.Refundable())
	}
}

func TestAReversalRefusalIsReportedWithItsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"description":"the payment has been fully refunded"}}`))
	}))
	defer server.Close()

	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	_, err = client.CreateRefund(t.Context(), "pay_1", 100, "key-one-two", nil)
	if err == nil {
		t.Fatal("a refused reversal must not read as success")
	}
	if !strings.Contains(err.Error(), "fully refunded") {
		t.Fatalf("error lost the gateway's reason: %v", err)
	}
}

func TestAReversalNeedsAPaymentAndAnAmount(t *testing.T) {
	client, err := NewClient("key", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateRefund(t.Context(), "", 100, "key-one-two", nil); err == nil {
		t.Fatal("a reversal with no payment should refuse")
	}
	if _, err := client.CreateRefund(t.Context(), "pay_1", 0, "key-one-two", nil); err == nil {
		t.Fatal("a reversal of nothing should refuse")
	}
	if _, err := client.Payment(t.Context(), " "); err == nil {
		t.Fatal("reading no payment should refuse")
	}
	if _, err := client.PaymentRefunds(t.Context(), " "); err == nil {
		t.Fatal("listing reversals for no payment should refuse")
	}
}

// The notes are the gateway's own copy of the trail. Reconciliation reads the
// order back out of them, and the run a reversal is owed under lives there rather
// than in the retry key so that a replay inside one run is exact. Nothing
// asserted they reach the wire: dropping them from the body left every test in
// this repository green while every reversal went out anonymous.
func TestAReversalCarriesItsTrailIntoTheGatewayBody(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"reversal-1","payment_id":"pay_1","amount":45000,"status":"processed"}`))
	}))
	defer server.Close()

	client, err := NewClient("key", "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL

	notes := map[string]string{
		"order_id":      "order-1",
		"account_id":    "account-9",
		"run_id":        "run-4",
		"reason":        "Cancelled by user",
		"part_of_paise": "80000",
	}
	if _, err := client.CreateRefund(t.Context(), "pay_1", 45000, "telegram:42:refund:7", notes); err != nil {
		t.Fatal(err)
	}

	var sent struct {
		Amount int64             `json:"amount"`
		Speed  string            `json:"speed"`
		Notes  map[string]string `json:"notes"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if sent.Amount != 45000 {
		t.Fatalf("body carried amount %d, want the amount asked for", sent.Amount)
	}
	// Instant costs the merchant a fee and is irreversible sooner. The normal
	// speed is the deliberate choice, so it is stated rather than left to whatever
	// the gateway defaults to this year.
	if sent.Speed != "normal" {
		t.Fatalf("body carried speed %q, want normal", sent.Speed)
	}
	for key, want := range notes {
		if sent.Notes[key] != want {
			t.Fatalf("notes[%q] = %q, want %q. These are the only record tying a refund back to the order, the account and the run it is owed under.", key, sent.Notes[key], want)
		}
	}
}
