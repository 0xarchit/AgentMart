// Tests for wallet request validation.
package wallet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/runid"
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
		_, _ = w.Write([]byte(`{"approved":true,"order_id":"order"}`))
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(db).Refund(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestFulfillRejectsDatabaseDecision(t *testing.T) {
	tests := []string{"stock is insufficient", "wallet balance is insufficient"}
	for _, reason := range tests {
		t.Run(reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(FulfillResult{Approved: false, Reason: reason})
			}))
			defer server.Close()

			db, err := supabase.NewClient(server.URL, "secret", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			request := FulfillRequest{AccountID: "account", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 100, RazorpayOrderID: "artifact", IdempotencyKey: "purchase", RefundWindowMinutes: 60}
			_, err = NewService(db).Fulfill(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("expected rejection %q, got %v", reason, err)
			}
		})
	}
}

func TestTopUpUsesVerifiedCreditRPC(t *testing.T) {
	request := TopUpRequest{AccountID: "account", AmountPaise: 10000, IdempotencyKey: "payment", RazorpayOrderID: "order", RazorpayPaymentID: "payment-id"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/rpc/credit_wallet_topup" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload TopUpRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload != request {
			t.Fatalf("payload = %+v", payload)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := NewService(db).TopUp(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestFulfillUsesAtomicWalletRPC(t *testing.T) {
	request := FulfillRequest{AccountID: "account", ProductID: "product", Quantity: 2, BaseAmountPaise: 200, FinalAmountPaise: 240, RazorpayOrderID: "artifact", IdempotencyKey: "purchase", RefundWindowMinutes: 60}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(FulfillResult{Approved: true, OrderID: "order"})

		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/rpc/fulfill_wallet_order" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing trusted authorization header")
		}
		var payload FulfillRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload != request {
			t.Fatalf("payload = %+v", payload)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(db).Fulfill(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

// fulfilmentSpy answers one fulfilment call and keeps the body it was sent.
func fulfilmentSpy(t *testing.T, sent *map[string]any) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(sent); err != nil {
			t.Fatalf("decode call: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"approved":true,"order_id":"11111111-1111-1111-1111-111111111111"}`))
	}))
	t.Cleanup(server.Close)
	db, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewService(db)
}

func settlement() FulfillRequest {
	return FulfillRequest{
		AccountID: "22222222-2222-2222-2222-222222222222", ProductID: "33333333-3333-3333-3333-333333333333",
		Quantity: 1, BaseAmountPaise: 45000, FinalAmountPaise: 45000,
		RazorpayOrderID: "artifact", IdempotencyKey: "key", RefundWindowMinutes: 60,
	}
}

func TestAMoneyRowCarriesTheRunItCameFrom(t *testing.T) {
	var sent map[string]any
	service := fulfilmentSpy(t, &sent)

	ctx := runid.With(t.Context(), "run-under-test")
	if _, err := service.Fulfill(ctx, settlement()); err != nil {
		t.Fatal(err)
	}
	if sent["p_run_id"] != "run-under-test" {
		t.Fatalf("p_run_id = %v, want the run the purchase belongs to: revenue that cannot be traced to a conversation cannot be explained", sent["p_run_id"])
	}
}

func TestACallerCannotOverrideTheRunOnAMoneyRow(t *testing.T) {
	var sent map[string]any
	service := fulfilmentSpy(t, &sent)

	request := settlement()
	request.RunID = "a-run-the-caller-invented"
	if _, err := service.Fulfill(runid.With(t.Context(), "the-real-run"), request); err != nil {
		t.Fatal(err)
	}
	if sent["p_run_id"] != "the-real-run" {
		t.Fatalf("p_run_id = %v, want the surrounding run", sent["p_run_id"])
	}
}

func TestASettlementOutsideAnyRunLeavesTheRunUnset(t *testing.T) {
	var sent map[string]any
	service := fulfilmentSpy(t, &sent)

	if _, err := service.Fulfill(t.Context(), settlement()); err != nil {
		t.Fatal(err)
	}
	if _, present := sent["p_run_id"]; present {
		t.Fatalf("p_run_id was sent as %v, want it omitted so the stored default applies", sent["p_run_id"])
	}
}
