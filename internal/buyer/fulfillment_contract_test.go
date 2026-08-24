// Tests for persisted approval and atomic wallet fulfillment contracts.
package buyer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/supabase"
	"agentmart/internal/wallet"
)

func TestApprovalResumeUsesAtomicWalletFulfillment(t *testing.T) {
	var pending ApprovalRequest
	var fulfilled wallet.FulfillRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("apikey") != "secret" {
			t.Error("trusted Supabase headers were not sent")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/rest/v1/rpc/create_human_approval":
			if err := json.NewDecoder(r.Body).Decode(&pending); err != nil {
				t.Errorf("decode approval request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(ApprovalResult{Approved: true, Token: pending.Token})
		case "/rest/v1/rpc/resolve_human_approval":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode approval resolution: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request["p_token"] != pending.Token || request["p_decision"] != "approve" || request["p_telegram_id"] != float64(pending.TelegramID) {
				t.Errorf("unexpected approval resolution payload: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(ApprovalResolution{
				Resolved:         true,
				Approved:         true,
				AccountID:        pending.AccountID,
				ProductID:        pending.ProductID,
				Quantity:         pending.Quantity,
				BaseAmountPaise:  pending.BaseAmountPaise,
				FinalAmountPaise: pending.FinalAmountPaise,
				IdempotencyKey:   pending.IdempotencyKey,
			})
		case "/rest/v1/rpc/fulfill_wallet_order":
			if err := json.NewDecoder(r.Body).Decode(&fulfilled); err != nil {
				t.Errorf("decode fulfillment request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		default:
			t.Errorf("unexpected Supabase path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("create Supabase client: %v", err)
	}
	approvals := NewApprovalStore(db)

	beforeRestart := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{}, &fakeArtifacts{}, &fakeWallet{}, approvals)
	created, err := beforeRestart.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, IdempotencyKey: "approval-fulfillment"})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	if !created.ApprovalRequired || created.ApprovalToken == "" {
		t.Fatalf("expected persisted approval, got %+v", created)
	}

	artifacts := &fakeArtifacts{}
	afterRestart := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, wallet.NewService(db), approvals)
	result, err := afterRestart.ResolveApproval(t.Context(), 1, created.ApprovalToken, "approve")
	if err != nil {
		t.Fatalf("resume approved purchase: %v", err)
	}

	if !result.Fulfilled || result.AmountPaise != 140 || artifacts.calls != 1 {
		t.Fatalf("expected fulfilled approved purchase, got %+v", result)
	}
	if fulfilled.AccountID != "account" || fulfilled.ProductID != "product" || fulfilled.Quantity != 1 {
		t.Fatalf("unexpected fulfillment identity: %+v", fulfilled)
	}
	if fulfilled.BaseAmountPaise != 100 || fulfilled.FinalAmountPaise != 140 || fulfilled.IdempotencyKey != "approval-fulfillment" {
		t.Fatalf("approval contract was not preserved in fulfillment: %+v", fulfilled)
	}
	if fulfilled.RazorpayOrderID != "order" || fulfilled.RefundWindowMinutes != 60 {
		t.Fatalf("unexpected fulfillment artifact contract: %+v", fulfilled)
	}
}

func TestPurchaseDoesNotSucceedWhenAtomicFulfillmentRejects(t *testing.T) {
	fulfillmentCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/rpc/fulfill_wallet_order" {
			t.Errorf("unexpected Supabase path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fulfillmentCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"P0001","message":"stock is insufficient"}`))
	}))
	t.Cleanup(server.Close)

	db, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("create Supabase client: %v", err)
	}

	artifacts := &fakeArtifacts{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, wallet.NewService(db))
	result, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, IdempotencyKey: "rejected-fulfillment"})
	if err == nil {
		t.Fatal("expected atomic fulfillment rejection")
	}
	if !fulfillmentCalled {
		t.Fatal("expected atomic fulfillment RPC call")
	}
	if result.Fulfilled || result.RazorpayOrderID != "" {
		t.Fatalf("rejected fulfillment reported success: %+v", result)
	}
	if artifacts.calls != 1 {
		t.Fatalf("expected one payment artifact before atomic fulfillment, got %d", artifacts.calls)
	}
}
