// Package gate owns deterministic purchase authorization policy.
package gate

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Request contains trusted purchase facts gathered outside buyer reasoning.
type Request struct {
	AccountID          string
	ProductID          string
	Quantity           int
	UnitPricePaise     int64
	BaseAmountPaise    int64
	FinalAmountPaise   int64
	WalletBalancePaise int64
	SpendLimitPaise    int64
	Stock              int
	PriceObservedAt    time.Time
	Now                time.Time
}

// Decision records the Gate outcome and stable reason code.
type Decision struct {
	Approved bool
	Reason   string
	Request  Request
}

// Auditor persists every approved and rejected Gate decision.
type Auditor interface {
	RecordGateDecision(context.Context, Decision) error
}

// Gate evaluates purchase policy independently from buyer reasoning.
type Gate struct {
	auditor     Auditor
	maxPriceAge time.Duration
}

// New constructs a Gate with mandatory auditing.
func New(auditor Auditor, maxPriceAge time.Duration) (*Gate, error) {
	if auditor == nil {
		return nil, fmt.Errorf("gate auditor is required")
	}
	if maxPriceAge <= 0 {
		return nil, fmt.Errorf("maximum price age must be positive")
	}
	return &Gate{auditor: auditor, maxPriceAge: maxPriceAge}, nil
}

// Evaluate authorizes or rejects a purchase and audits the result before returning.
func (g *Gate) Evaluate(ctx context.Context, request Request) (Decision, error) {
	decision := Decision{Request: request, Reason: rejectionReason(request, g.maxPriceAge)}
	decision.Approved = decision.Reason == "approved"
	if err := g.auditor.RecordGateDecision(ctx, decision); err != nil {
		return Decision{}, fmt.Errorf("audit gate decision: %w", err)
	}
	return decision, nil
}

func rejectionReason(request Request, maxPriceAge time.Duration) string {
	if request.AccountID == "" || request.ProductID == "" {
		return "missing_identity"
	}
	if request.Quantity <= 0 {
		return "invalid_quantity"
	}
	if request.UnitPricePaise <= 0 || request.BaseAmountPaise <= 0 || request.FinalAmountPaise <= 0 {
		return "invalid_amount"
	}
	if request.UnitPricePaise > math.MaxInt64/int64(request.Quantity) {
		return "amount_overflow"
	}
	if request.BaseAmountPaise != request.UnitPricePaise*int64(request.Quantity) {
		return "amount_mismatch"
	}
	if request.FinalAmountPaise < request.BaseAmountPaise {
		return "negotiated_amount_below_base"
	}
	if request.Stock < request.Quantity {
		return "insufficient_stock"
	}
	if request.SpendLimitPaise <= 0 || request.FinalAmountPaise > request.SpendLimitPaise {
		return "human_approval_required"
	}
	if request.WalletBalancePaise < request.FinalAmountPaise {
		return "insufficient_wallet_balance"
	}
	if request.Now.IsZero() || request.PriceObservedAt.IsZero() || request.Now.Sub(request.PriceObservedAt) > maxPriceAge || request.Now.Before(request.PriceObservedAt) {
		return "stale_price"
	}
	return "approved"
}
