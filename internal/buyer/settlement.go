// The one settlement step behind the gate. Everything above the gate approves an
// amount and never learns what moved it.
package buyer

import (
	"context"
	"fmt"

	"agentmart/internal/wallet"
)

// SettleRequest is an amount the gate has already approved, with the identity of
// what it buys. Nothing here is chosen by a reasoning layer.
type SettleRequest struct {
	AccountID        string
	ProductID        string
	Quantity         int
	BaseAmountPaise  int64
	FinalAmountPaise int64
	IdempotencyKey   string
}

// SettleResult is what a settled purchase leaves behind: the order row it wrote,
// the gateway object it can be checked against, and how it was settled.
type SettleResult struct {
	OrderID        string
	GatewayOrderID string
	Method         string
}

// Settlement moves money for an approved amount. One implementation spends the
// prepaid allowance. A second one charging a granted mandate drops in here
// without anything above the gate changing.
type Settlement interface {
	Settle(context.Context, SettleRequest) (SettleResult, error)
}

// WalletSettlement spends the prepaid allowance the person funded with a real
// captured payment, and records a gateway object per purchase for the trail.
type WalletSettlement struct {
	artifacts artifactCreator
	wallet    walletFulfiller
}

// NewWalletSettlement builds the allowance settlement path.
func NewWalletSettlement(artifacts artifactCreator, walletService walletFulfiller) *WalletSettlement {
	return &WalletSettlement{artifacts: artifacts, wallet: walletService}
}

// Settle debits the allowance for an amount the gate approved.
func (w *WalletSettlement) Settle(ctx context.Context, request SettleRequest) (SettleResult, error) {
	if w.artifacts == nil || w.wallet == nil {
		return SettleResult{}, fmt.Errorf("settlement path is unavailable")
	}
	receipt := "wallet_" + request.IdempotencyKey
	if len(receipt) > 40 {
		receipt = receipt[:40]
	}
	artifact, err := w.artifacts.CreateWalletArtifact(ctx, request.FinalAmountPaise, receipt, map[string]string{"account_id": request.AccountID, "product_id": request.ProductID, "fulfillment": "wallet"})
	if err != nil {
		return SettleResult{}, err
	}
	orderID, err := w.wallet.Fulfill(ctx, wallet.FulfillRequest{AccountID: request.AccountID, ProductID: request.ProductID, Quantity: request.Quantity, BaseAmountPaise: request.BaseAmountPaise, FinalAmountPaise: request.FinalAmountPaise, RazorpayOrderID: artifact.ID, IdempotencyKey: request.IdempotencyKey, RefundWindowMinutes: 60})
	if err != nil {
		return SettleResult{}, err
	}
	return SettleResult{OrderID: orderID, GatewayOrderID: artifact.ID, Method: "fulfilled_via_wallet"}, nil
}
