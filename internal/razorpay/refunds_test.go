// Proves the reversal path: where it posts, what it sends, that a retry carries a
// key the gateway will accept, and that only a captured payment has anything left
// to give back.
package razorpay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
}

func TestARetryKeyIsShapedRatherThanRejectedOnArrival(t *testing.T) {
	shaped := reversalKey("telegram:42:refund:7")
	if strings.Contains(shaped, ":") {
		t.Fatalf("key %q still carries a character the gateway rejects", shaped)
	}
	for _, r := range shaped {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !allowed {
			t.Fatalf("key %q contains %q", shaped, r)
		}
	}
	if short := reversalKey("ab:c"); len(short) < 10 {
		t.Fatalf("short key %q is below the ten character minimum", short)
	}
	if reversalKey("   ") != "" {
		t.Fatal("an empty key should stay empty rather than become padding")
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
}
