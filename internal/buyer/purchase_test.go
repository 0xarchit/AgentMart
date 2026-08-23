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

func (f *fakeWallet) Fulfill(_ context.Context, request wallet.FulfillRequest) error {
	f.calls++
	f.request = request
	return nil
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
