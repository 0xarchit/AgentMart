// Tests for straight-through wallet purchase orchestration.
package buyer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/razorpay"
	"agentmart/internal/supabase"
	"agentmart/internal/wallet"
)

type fakeCatalog struct{}

func (fakeCatalog) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "product", PricePaise: 100, Stock: 2}, nil
}

type fakeAccounts struct{}

func (fakeAccounts) AccountForTelegram(context.Context, int64) (Account, error) {
	return Account{ID: "account", WalletBalancePaise: 500, SpendLimitPaise: 500}, nil
}

type fakeGate struct{ approved bool }

func (f fakeGate) Evaluate(_ context.Context, request gate.Request) (gate.Decision, error) {
	reason := "human_approval_required"
	if f.approved {
		reason = "approved"
	}
	return gate.Decision{Approved: f.approved, Reason: reason, Request: request}, nil
}

type fakeArtifacts struct {
	calls       int
	amountPaise int64
}

func (f *fakeArtifacts) CreateWalletArtifact(_ context.Context, amountPaise int64, _ string, _ map[string]string) (razorpay.Order, error) {
	f.calls++
	f.amountPaise = amountPaise
	return razorpay.Order{ID: "order"}, nil
}

type fakeWallet struct {
	calls   int
	request wallet.FulfillRequest
}

type fakeApprovals struct{ request ApprovalRequest }

func (f *fakeApprovals) Create(_ context.Context, request ApprovalRequest) (ApprovalResult, error) {
	f.request = request
	return ApprovalResult{Approved: true, Token: request.Token}, nil
}

func (f *fakeApprovals) Resolve(_ context.Context, _ int64, _ string, decision string) (ApprovalResolution, error) {
	return ApprovalResolution{Resolved: true, Approved: decision == "approve", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, IdempotencyKey: "key"}, nil
}

func (f *fakeWallet) Fulfill(_ context.Context, request wallet.FulfillRequest) (string, error) {
	f.calls++
	f.request = request
	return "order-1", nil
}

func TestPurchaseFulfillsAfterApproval(t *testing.T) {
	artifacts := &fakeArtifacts{}
	walletService := &fakeWallet{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, walletService)
	result, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fulfilled || artifacts.calls != 1 || walletService.calls != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPurchaseFulfillsAcceptedNegotiatedAmount(t *testing.T) {
	artifacts := &fakeArtifacts{}
	walletService := &fakeWallet{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, walletService)
	result, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AmountPaise != 140 || artifacts.amountPaise != 140 {
		t.Fatalf("result = %+v, artifact amount = %d", result, artifacts.amountPaise)
	}
	if walletService.request.BaseAmountPaise != 100 || walletService.request.FinalAmountPaise != 140 {
		t.Fatalf("fulfillment = %+v", walletService.request)
	}
}

func TestPurchaseStopsAfterGateRejection(t *testing.T) {
	artifacts := &fakeArtifacts{}
	walletService := &fakeWallet{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{}, artifacts, walletService)
	result, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Fulfilled || artifacts.calls != 0 || walletService.calls != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPurchaseCreatesApprovalForLimitRejection(t *testing.T) {
	approvals := &fakeApprovals{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{}, &fakeArtifacts{}, &fakeWallet{}, approvals)
	result, err := service.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ApprovalRequired || result.ApprovalToken == "" || approvals.request.FinalAmountPaise != 100 {
		t.Fatalf("result = %+v, request = %+v", result, approvals.request)
	}
}

func TestResolveApprovalResumesPurchase(t *testing.T) {
	artifacts := &fakeArtifacts{}
	walletService := &fakeWallet{}
	approvals := &fakeApprovals{}
	service := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, walletService, approvals)
	result, err := service.ResolveApproval(t.Context(), 1, "token", "approve")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fulfilled || result.AmountPaise != 140 || walletService.request.FinalAmountPaise != 140 {
		t.Fatalf("result = %+v, fulfillment = %+v", result, walletService.request)
	}
}

func TestApprovalResumesWithFreshPurchaseService(t *testing.T) {
	var pending ApprovalRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/v1/rpc/create_human_approval":
			if err := json.NewDecoder(r.Body).Decode(&pending); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(ApprovalResult{Approved: true, Token: pending.Token})
		case "/rest/v1/rpc/resolve_human_approval":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["p_token"] != pending.Token {
				t.Fatalf("token = %v", request["p_token"])
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
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	firstDB, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{}, &fakeArtifacts{}, &fakeWallet{}, NewApprovalStore(firstDB))
	created, err := first.Purchase(t.Context(), PurchaseRequest{TelegramID: 1, ProductID: "product", Quantity: 1, IdempotencyKey: "restart-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !created.ApprovalRequired || created.ApprovalToken == "" {
		t.Fatalf("created = %+v", created)
	}

	secondDB, err := supabase.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	artifacts := &fakeArtifacts{}
	walletService := &fakeWallet{}
	second := NewPurchaseService(fakeCatalog{}, fakeAccounts{}, fakeGate{approved: true}, artifacts, walletService, NewApprovalStore(secondDB))
	result, err := second.ResolveApproval(t.Context(), 1, created.ApprovalToken, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fulfilled || artifacts.calls != 1 || walletService.calls != 1 || walletService.request.IdempotencyKey != "restart-key" {
		t.Fatalf("result = %+v, fulfillment = %+v", result, walletService.request)
	}
}
