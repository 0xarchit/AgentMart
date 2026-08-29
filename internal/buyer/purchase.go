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
	Fulfill(context.Context, wallet.FulfillRequest) (string, error)
}
type approvalCreator interface {
	Create(context.Context, ApprovalRequest) (ApprovalResult, error)
}
type approvalResolver interface {
	Resolve(context.Context, int64, string, string) (ApprovalResolution, error)
}

// PurchaseRequest identifies one Telegram purchase attempt.
type PurchaseRequest struct {
	TelegramID       int64
	ProductID        string
	Quantity         int
	BaseAmountPaise  int64
	FinalAmountPaise int64
	IdempotencyKey   string
	HumanApproved    bool
}

// PurchaseResult reports the stable purchase outcome.
type PurchaseResult struct {
	Fulfilled        bool
	ApprovalRequired bool
	ApprovalToken    string
	Reason           string
	AmountPaise      int64
	RazorpayOrderID  string
	// OrderID is the order row this purchase created, which is what a
	// cancellation is keyed on.
	OrderID string
}

// PurchaseService executes the trusted purchase sequence.
type PurchaseService struct {
	catalog   catalogReader
	accounts  accountReader
	gate      gateEvaluator
	settle    Settlement
	approvals approvalCreator
	resolver  approvalResolver
	now       func() time.Time
}

// NewPurchaseService constructs the straight-through buyer workflow, settling
// through the prepaid allowance.
func NewPurchaseService(catalog catalogReader, accounts accountReader, gateService gateEvaluator, artifacts artifactCreator, walletService walletFulfiller, approvalStores ...approvalCreator) *PurchaseService {
	var approvals approvalCreator
	if len(approvalStores) > 0 {
		approvals = approvalStores[0]
	}
	resolver, _ := approvals.(approvalResolver)
	return &PurchaseService{catalog: catalog, accounts: accounts, gate: gateService, settle: NewWalletSettlement(artifacts, walletService), approvals: approvals, resolver: resolver, now: time.Now}
}

// UseSettlement replaces what moves the money once the gate has approved an
// amount. The rest of the sequence, including every bound the gate applies, is
// unchanged by the swap.
func (s *PurchaseService) UseSettlement(settlement Settlement) {
	if settlement != nil {
		s.settle = settlement
	}
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
	decision, err := s.gate.Evaluate(ctx, gate.Request{AccountID: account.ID, ProductID: product.ID, Quantity: request.Quantity, UnitPricePaise: product.PricePaise, BaseAmountPaise: baseAmount, FinalAmountPaise: finalAmount, WalletBalancePaise: account.WalletBalancePaise, SpendLimitPaise: account.SpendLimitPaise, HumanApproved: request.HumanApproved, Stock: product.Stock, PriceObservedAt: now, Now: now})
	if err != nil {
		return PurchaseResult{}, err
	}
	if !decision.Approved {
		if decision.Reason == "human_approval_required" && s.approvals != nil {
			approvalRequest, err := NewApprovalRequest(account, request.TelegramID, product, request.Quantity, baseAmount, finalAmount, request.IdempotencyKey, decision.Reason)
			if err != nil {
				return PurchaseResult{}, err
			}
			approval, err := s.approvals.Create(ctx, approvalRequest)
			if err != nil {
				return PurchaseResult{}, err
			}
			return PurchaseResult{ApprovalRequired: true, ApprovalToken: approval.Token, Reason: decision.Reason, AmountPaise: finalAmount}, nil
		}
		return PurchaseResult{Reason: decision.Reason, AmountPaise: finalAmount}, nil
	}
	settled, err := s.settle.Settle(ctx, SettleRequest{AccountID: account.ID, ProductID: product.ID, Quantity: request.Quantity, BaseAmountPaise: baseAmount, FinalAmountPaise: finalAmount, IdempotencyKey: request.IdempotencyKey})
	if err != nil {
		return PurchaseResult{}, err
	}
	return PurchaseResult{Fulfilled: true, Reason: settled.Method, AmountPaise: finalAmount, RazorpayOrderID: settled.GatewayOrderID, OrderID: settled.OrderID}, nil
}

// RequestApproval records a pending approval without evaluating the Gate for an
// auto-buy. It exists for the case where the buyer agent itself decided the
// human should own this call: the person then resolves it with /approve or
// /reject, and the resumed purchase goes through the Gate as usual.
func (s *PurchaseService) RequestApproval(ctx context.Context, request PurchaseRequest, reason string) (PurchaseResult, error) {
	if s.approvals == nil {
		return PurchaseResult{}, fmt.Errorf("human approval store is unavailable")
	}
	if request.TelegramID <= 0 || strings.TrimSpace(request.ProductID) == "" || request.Quantity <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		return PurchaseResult{}, fmt.Errorf("Telegram id, product id, quantity, and idempotency key are required")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "buyer agent asked for human confirmation"
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
	// Same amount integrity rules as a purchase: the approval locks exactly what
	// the human is being asked to authorise.
	if baseAmount != amount || finalAmount < baseAmount {
		return PurchaseResult{}, fmt.Errorf("negotiated amount is invalid")
	}
	approvalRequest, err := NewApprovalRequest(account, request.TelegramID, product, request.Quantity, baseAmount, finalAmount, request.IdempotencyKey, reason)
	if err != nil {
		return PurchaseResult{}, err
	}
	approval, err := s.approvals.Create(ctx, approvalRequest)
	if err != nil {
		return PurchaseResult{}, err
	}
	return PurchaseResult{ApprovalRequired: true, ApprovalToken: approval.Token, Reason: reason, AmountPaise: finalAmount}, nil
}

// ResolveApproval applies a Telegram decision and resumes the original purchase.
func (s *PurchaseService) ResolveApproval(ctx context.Context, telegramID int64, token string, decision string) (PurchaseResult, error) {
	if s.resolver == nil {
		return PurchaseResult{}, fmt.Errorf("human approval resolver is unavailable")
	}
	resolution, err := s.resolver.Resolve(ctx, telegramID, token, decision)
	if err != nil {
		return PurchaseResult{}, err
	}
	if !resolution.Resolved {
		return PurchaseResult{Reason: resolution.Reason}, nil
	}
	if !resolution.Approved {
		return PurchaseResult{Reason: "human approval rejected"}, nil
	}
	return s.Purchase(ctx, PurchaseRequest{TelegramID: telegramID, ProductID: resolution.ProductID, Quantity: resolution.Quantity, BaseAmountPaise: resolution.BaseAmountPaise, FinalAmountPaise: resolution.FinalAmountPaise, IdempotencyKey: resolution.IdempotencyKey, HumanApproved: true})
}
