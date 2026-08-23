// Straight-through purchase orchestration after independent Gate approval.
package buyer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/gate"
	"agentmart/internal/razorpay"
	"agentmart/internal/wallet"
)

type catalogReader interface {
	Get(context.Context, string) (catalog.Product, error)
}
type accountReader interface {
	AccountForTelegram(context.Context, int64) (Account, error)
}
type gateEvaluator interface {
	Evaluate(context.Context, gate.Request) (gate.Decision, error)
}
type artifactCreator interface {
	CreateWalletArtifact(context.Context, int64, string, map[string]string) (razorpay.Order, error)
}
type walletFulfiller interface {
	Fulfill(context.Context, wallet.FulfillRequest) error
}

// PurchaseRequest identifies one Telegram purchase attempt.
type PurchaseRequest struct {
	TelegramID       int64
	ProductID        string
	Quantity         int
	BaseAmountPaise  int64
	FinalAmountPaise int64
	IdempotencyKey   string
}

// PurchaseResult reports the stable purchase outcome.
type PurchaseResult struct {
	Fulfilled       bool
	Reason          string
	AmountPaise     int64
	RazorpayOrderID string
}

// PurchaseService executes the trusted purchase sequence.
type PurchaseService struct {
	catalog   catalogReader
	accounts  accountReader
	gate      gateEvaluator
	artifacts artifactCreator
	wallet    walletFulfiller
	now       func() time.Time
}

// NewPurchaseService constructs the straight-through buyer workflow.
func NewPurchaseService(catalog catalogReader, accounts accountReader, gateService gateEvaluator, artifacts artifactCreator, walletService walletFulfiller) *PurchaseService {
	return &PurchaseService{catalog: catalog, accounts: accounts, gate: gateService, artifacts: artifacts, wallet: walletService, now: time.Now}
}

// Purchase evaluates and fulfills one wallet-backed order.
func (s *PurchaseService) Purchase(ctx context.Context, request PurchaseRequest) (PurchaseResult, error) {
	if request.TelegramID <= 0 || strings.TrimSpace(request.ProductID) == "" || request.Quantity <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		return PurchaseResult{}, fmt.Errorf("Telegram id, product id, quantity, and idempotency key are required")
	}
	account, err := s.accounts.AccountForTelegram(ctx, request.TelegramID)
	if err != nil {
		return PurchaseResult{}, err
	}
	product, err := s.catalog.Get(ctx, request.ProductID)
	if err != nil {
		return PurchaseResult{}, err
	}
	if product.PricePaise > math.MaxInt64/int64(request.Quantity) {
		return PurchaseResult{}, fmt.Errorf("purchase amount overflow")
	}
	amount := product.PricePaise * int64(request.Quantity)
	baseAmount := request.BaseAmountPaise
	if baseAmount == 0 {
		baseAmount = amount
	}
	finalAmount := request.FinalAmountPaise
	if finalAmount == 0 {
		finalAmount = amount
	}
	if baseAmount != amount || finalAmount < baseAmount {
		return PurchaseResult{}, fmt.Errorf("negotiated amount is invalid")
	}
	now := s.now()
	decision, err := s.gate.Evaluate(ctx, gate.Request{AccountID: account.ID, ProductID: product.ID, Quantity: request.Quantity, UnitPricePaise: product.PricePaise, BaseAmountPaise: baseAmount, FinalAmountPaise: finalAmount, WalletBalancePaise: account.WalletBalancePaise, SpendLimitPaise: account.SpendLimitPaise, Stock: product.Stock, PriceObservedAt: now, Now: now})
	if err != nil {
		return PurchaseResult{}, err
	}
	if !decision.Approved {
		return PurchaseResult{Reason: decision.Reason, AmountPaise: finalAmount}, nil
	}
	receipt := "wallet_" + request.IdempotencyKey
	if len(receipt) > 40 {
		receipt = receipt[:40]
	}
	artifact, err := s.artifacts.CreateWalletArtifact(ctx, finalAmount, receipt, map[string]string{"account_id": account.ID, "product_id": product.ID, "fulfillment": "wallet"})
	if err != nil {
		return PurchaseResult{}, err
	}
	err = s.wallet.Fulfill(ctx, wallet.FulfillRequest{AccountID: account.ID, ProductID: product.ID, Quantity: request.Quantity, BaseAmountPaise: baseAmount, FinalAmountPaise: finalAmount, RazorpayOrderID: artifact.ID, IdempotencyKey: request.IdempotencyKey, RefundWindowMinutes: 60})
	if err != nil {
		return PurchaseResult{}, err
	}
	return PurchaseResult{Fulfilled: true, Reason: "fulfilled_via_wallet", AmountPaise: finalAmount, RazorpayOrderID: artifact.ID}, nil
}
