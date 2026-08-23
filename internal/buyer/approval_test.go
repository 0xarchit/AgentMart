// Tests for persisted human approval request construction.
package buyer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/supabase"
)

func TestNewApprovalRequestGeneratesResumeToken(t *testing.T) {
	account := Account{ID: "account"}
	product := catalog.Product{ID: "product", PricePaise: 100}
	first, err := NewApprovalRequest(account, 10, product, 1, 100, 140, "purchase-1", "human approval required")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewApprovalRequest(account, 10, product, 1, 100, 140, "purchase-2", "human approval required")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" || len(first.Token) != 32 || first.Token == second.Token {
		t.Fatalf("tokens = %q and %q", first.Token, second.Token)
	}
	if err := validateApprovalRequest(first); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalRequestRejectsDiscount(t *testing.T) {
	err := validateApprovalRequest(ApprovalRequest{Token: "token", AccountID: "account", TelegramID: 10, ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 99, IdempotencyKey: "purchase", Reason: "approval"})
	if err == nil {
		t.Fatal("expected discount rejection")
	}
}

func TestApprovalStoreCreateUsesApprovalRPC(t *testing.T) {
	request := ApprovalRequest{Token: "token", AccountID: "account", TelegramID: 10, ProductID: "product", Quantity: 2, BaseAmountPaise: 200, FinalAmountPaise: 240, IdempotencyKey: "purchase", Reason: "human approval required"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/rpc/create_human_approval" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing trusted authorization header")
		}
		var payload ApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload != request {
			t.Fatalf("payload = %+v", payload)
		}
		_, _ = w.Write([]byte(`{"approved":true,"approval_id":"approval","token":"token","expires_at":"2026-08-24T12:00:00Z"}`))
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewApprovalStore(db).Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved || result.ApprovalID != "approval" || result.Token != request.Token || result.ExpiresAt == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestApprovalStoreResolveUsesDecisionRPC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/rpc/resolve_human_approval" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["p_token"] != "token" || payload["p_decision"] != "approve" || payload["p_telegram_id"] != float64(10) {
			t.Fatalf("payload = %+v", payload)
		}
		_, _ = w.Write([]byte(`{"resolved":true,"approved":true,"account_id":"account","product_id":"product","qty":2,"base_amount_paise":200,"final_amount_paise":240,"idempotency_key":"purchase"}`))
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewApprovalStore(db).Resolve(t.Context(), 10, "token", "approve")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resolved || !result.Approved || result.AccountID != "account" || result.ProductID != "product" || result.Quantity != 2 || result.FinalAmountPaise != 240 || result.IdempotencyKey != "purchase" {
		t.Fatalf("result = %+v", result)
	}
}
