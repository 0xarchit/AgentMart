// Tests for wallet request validation.
package wallet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/supabase"
)

func TestFulfillRejectsDiscountBelowProposal(t *testing.T) {
	err := (FulfillRequest{AccountID: "a", ProductID: "p", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 99, RazorpayOrderID: "r", IdempotencyKey: "k", RefundWindowMinutes: 60}).validate()
	if err == nil {
		t.Fatal("expected final amount validation error")
	}
}

func TestTopUpRequiresVerifiedPaymentIDs(t *testing.T) {
	err := (TopUpRequest{AccountID: "a", AmountPaise: 100, IdempotencyKey: "k"}).validate()
	if err == nil {
		t.Fatal("expected payment id validation error")
	}
}

func TestRefundRequiresIdempotencyKey(t *testing.T) {
	err := (RefundRequest{AccountID: "a", OrderID: "o", Reason: "changed mind"}).validate()
	if err == nil {
		t.Fatal("expected refund idempotency validation error")
	}
}

func TestRefundPayloadUsesAllContractFields(t *testing.T) {
	request := RefundRequest{AccountID: "account", OrderID: "order", Reason: "changed mind", IdempotencyKey: "telegram:7:9"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/rpc/refund_wallet_order" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"p_account_id", "p_order_id", "p_reason", "p_idempotency_key"} {
			if payload[field] == nil || payload[field] == "" {
				t.Fatalf("payload missing %s", field)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := NewService(db).Refund(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
