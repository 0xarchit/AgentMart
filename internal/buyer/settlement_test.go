// Pins the allowance settlement path that moved out of the purchase sequence:
// the receipt it derives, the gateway object it records, and the refund window.
package buyer

import (
	"context"
	"strings"
	"testing"

	"agentmart/internal/razorpay"
	"agentmart/internal/wallet"
)

type receiptRecorder struct {
	amountPaise int64
	receipt     string
	notes       map[string]string
}

func (r *receiptRecorder) CreateWalletArtifact(_ context.Context, amountPaise int64, receipt string, notes map[string]string) (razorpay.Order, error) {
	r.amountPaise = amountPaise
	r.receipt = receipt
	r.notes = notes
	return razorpay.Order{ID: "gateway-order"}, nil
}

func TestTheAllowancePathRecordsAGatewayObjectAndARefundWindow(t *testing.T) {
	artifacts := &receiptRecorder{}
	walletService := &fakeWallet{}
	settlement := NewWalletSettlement(artifacts, walletService)

	result, err := settlement.Settle(t.Context(), SettleRequest{AccountID: "account", ProductID: "product", Quantity: 2, BaseAmountPaise: 100, FinalAmountPaise: 140, IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Method != "fulfilled_via_wallet" || result.GatewayOrderID != "gateway-order" {
		t.Fatalf("result = %+v", result)
	}
	if artifacts.amountPaise != 140 || artifacts.receipt != "wallet_key" {
		t.Fatalf("artifact recorded %d against receipt %q", artifacts.amountPaise, artifacts.receipt)
	}
	if artifacts.notes["account_id"] != "account" || artifacts.notes["fulfillment"] != "wallet" {
		t.Fatalf("notes = %v", artifacts.notes)
	}
	want := wallet.FulfillRequest{AccountID: "account", ProductID: "product", Quantity: 2, BaseAmountPaise: 100, FinalAmountPaise: 140, RazorpayOrderID: "gateway-order", IdempotencyKey: "key", RefundWindowMinutes: 60}
	if walletService.request != want {
		t.Fatalf("debit = %+v, want %+v", walletService.request, want)
	}
}

func TestALongIdempotencyKeyStillYieldsAnAcceptableReceipt(t *testing.T) {
	artifacts := &receiptRecorder{}
	settlement := NewWalletSettlement(artifacts, &fakeWallet{})

	_, err := settlement.Settle(t.Context(), SettleRequest{AccountID: "account", ProductID: "product", Quantity: 1, FinalAmountPaise: 100, IdempotencyKey: strings.Repeat("k", 80)})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts.receipt) != 40 {
		t.Fatalf("receipt is %d characters: %q", len(artifacts.receipt), artifacts.receipt)
	}
}

func TestSettlingWithNoPathRefusesInsteadOfPanicking(t *testing.T) {
	if _, err := (&WalletSettlement{}).Settle(t.Context(), SettleRequest{FinalAmountPaise: 100}); err == nil {
		t.Fatal("a settlement with no path should refuse")
	}
}
