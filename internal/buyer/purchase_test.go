// Tests for straight-through wallet purchase orchestration.
package buyer

import (
	"context"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/razorpay"
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

type fakeArtifacts struct{ calls int }

func (f *fakeArtifacts) CreateWalletArtifact(context.Context, int64, string, map[string]string) (razorpay.Order, error) {
	f.calls++
	return razorpay.Order{ID: "order"}, nil
}

type fakeWallet struct{ calls int }

func (f *fakeWallet) Fulfill(context.Context, wallet.FulfillRequest) error { f.calls++; return nil }

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
