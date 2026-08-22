// Tests for wallet purchase order artifacts.
package razorpay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateWalletArtifactDisablesCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"payment_capture":0`) {
			t.Fatalf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"id":"order_test","amount":100,"currency":"INR","status":"created"}`))
	}))
	defer server.Close()
	client, _ := NewClient("id", "secret", server.Client())
	client.baseURL = server.URL
	order, err := client.CreateWalletArtifact(t.Context(), 100, "receipt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != "order_test" {
		t.Fatalf("order id = %q", order.ID)
	}
}
