// Package wallet provides trusted wallet mutation calls.
package wallet

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/supabase"
)

// Service invokes service-role-only wallet functions.
type Service struct {
	db *supabase.Client
}

// NewService constructs a trusted wallet service.
func NewService(db *supabase.Client) *Service {
	return &Service{db: db}
}

// TopUp credits a verified human wallet top-up exactly once by idempotency key.
func (s *Service) TopUp(ctx context.Context, request TopUpRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	return s.db.RPC(ctx, "credit_wallet_topup", request, nil)
}

// Fulfill debits the wallet and records a fulfilled-via-wallet order atomically.
func (s *Service) Fulfill(ctx context.Context, request FulfillRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	var result FulfillResult
	if err := s.db.RPC(ctx, "fulfill_wallet_order", request, &result); err != nil {
		return err
	}
	if !result.Approved {
		return fmt.Errorf("wallet fulfillment rejected: %s", result.Reason)
	}
	return nil
}

type FulfillResult struct {
	Approved  bool   `json:"approved"`
	Duplicate bool   `json:"duplicate"`
	OrderID   string `json:"order_id"`
	Reason    string `json:"reason"`
}

// Refund credits the wallet for an eligible fulfilled order atomically.
func (s *Service) Refund(ctx context.Context, request RefundRequest) (RefundResult, error) {
	if err := request.validate(); err != nil {
		return RefundResult{}, err
	}
	var result RefundResult
	if err := s.db.RPC(ctx, "refund_wallet_order", request, &result); err != nil {
		return RefundResult{}, err
	}
	return result, nil
}

// RefundResult reports whether a wallet refund was approved or already applied.
type RefundResult struct {
	Approved     bool   `json:"approved"`
	Duplicate    bool   `json:"duplicate"`
	OrderID      string `json:"order_id"`
	AmountPaise  int64  `json:"amount_paise"`
	BalancePaise int64  `json:"balance_paise"`
	Reason       string `json:"reason"`
}

// TopUpRequest identifies a verified human wallet funding event.
type TopUpRequest struct {
	AccountID         string `json:"p_account_id"`
	AmountPaise       int64  `json:"p_amount_paise"`
	IdempotencyKey    string `json:"p_idempotency_key"`
	RazorpayOrderID   string `json:"p_razorpay_order_id"`
	RazorpayPaymentID string `json:"p_razorpay_payment_id"`
}

func (r TopUpRequest) validate() error {
	if strings.TrimSpace(r.AccountID) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("account id and idempotency key are required")
	}
	if r.AmountPaise <= 0 {
		return fmt.Errorf("top-up amount must be positive")
	}
	if strings.TrimSpace(r.RazorpayOrderID) == "" || strings.TrimSpace(r.RazorpayPaymentID) == "" {
		return fmt.Errorf("razorpay order and payment ids are required")
	}
	return nil
}

// FulfillRequest contains the buyer proposal and accepted wallet amount.
type FulfillRequest struct {
	AccountID           string `json:"p_account_id"`
	ProductID           string `json:"p_product_id"`
	Quantity            int    `json:"p_qty"`
	BaseAmountPaise     int64  `json:"p_base_amount_paise"`
	FinalAmountPaise    int64  `json:"p_final_amount_paise"`
	RazorpayOrderID     string `json:"p_razorpay_order_id"`
	IdempotencyKey      string `json:"p_idempotency_key"`
	RefundWindowMinutes int    `json:"p_refund_window_minutes"`
}

func (r FulfillRequest) validate() error {
	if strings.TrimSpace(r.AccountID) == "" || strings.TrimSpace(r.ProductID) == "" {
		return fmt.Errorf("account id and product id are required")
	}
	if r.Quantity <= 0 || r.BaseAmountPaise <= 0 || r.FinalAmountPaise <= 0 {
		return fmt.Errorf("quantity and amounts must be positive")
	}
	if r.FinalAmountPaise < r.BaseAmountPaise {
		return fmt.Errorf("final amount cannot be below base amount")
	}
	if strings.TrimSpace(r.RazorpayOrderID) == "" || strings.TrimSpace(r.IdempotencyKey) == "" || r.RefundWindowMinutes <= 0 {
		return fmt.Errorf("razorpay order, idempotency key, and refund window are required")
	}
	return nil
}

// RefundRequest identifies an order eligible for a wallet credit.
type RefundRequest struct {
	AccountID      string `json:"p_account_id"`
	OrderID        string `json:"p_order_id"`
	Reason         string `json:"p_reason"`
	IdempotencyKey string `json:"p_idempotency_key"`
}

func (r RefundRequest) validate() error {
	if strings.TrimSpace(r.OrderID) == "" || strings.TrimSpace(r.AccountID) == "" || strings.TrimSpace(r.Reason) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("order id, account id, reason, and idempotency key are required")
	}
	return nil
}
