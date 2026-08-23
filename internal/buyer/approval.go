// Human approval requests persist above-limit purchase decisions for Telegram resume.
package buyer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"agentmart/internal/catalog"
	"agentmart/internal/supabase"
)

// ApprovalRequest identifies the purchase awaiting a human decision.
type ApprovalRequest struct {
	Token            string `json:"p_token"`
	AccountID        string `json:"p_account_id"`
	TelegramID       int64  `json:"p_telegram_id"`
	ProductID        string `json:"p_product_id"`
	Quantity         int    `json:"p_qty"`
	BaseAmountPaise  int64  `json:"p_base_amount_paise"`
	FinalAmountPaise int64  `json:"p_final_amount_paise"`
	IdempotencyKey   string `json:"p_idempotency_key"`
	Reason           string `json:"p_reason"`
}

// ApprovalResult reports the persisted approval token and expiry metadata.
type ApprovalResult struct {
	Approved   bool   `json:"approved"`
	Duplicate  bool   `json:"duplicate"`
	ApprovalID string `json:"approval_id"`
	Token      string `json:"token"`
	ExpiresAt  string `json:"expires_at"`
	Reason     string `json:"reason"`
}

// ApprovalResolution reports the decision and original purchase values.
type ApprovalResolution struct {
	Resolved         bool   `json:"resolved"`
	Approved         bool   `json:"approved"`
	Reason           string `json:"reason"`
	AccountID        string `json:"account_id"`
	ProductID        string `json:"product_id"`
	Quantity         int    `json:"qty"`
	BaseAmountPaise  int64  `json:"base_amount_paise"`
	FinalAmountPaise int64  `json:"final_amount_paise"`
	IdempotencyKey   string `json:"idempotency_key"`
}

// ApprovalStore persists approval requests through the trusted service client.
type ApprovalStore struct {
	db *supabase.Client
}

// NewApprovalStore constructs an approval persistence service.
func NewApprovalStore(db *supabase.Client) *ApprovalStore { return &ApprovalStore{db: db} }

// Create records an approval request once and returns its resume token.
func (s *ApprovalStore) Create(ctx context.Context, request ApprovalRequest) (ApprovalResult, error) {
	if err := validateApprovalRequest(request); err != nil {
		return ApprovalResult{}, err
	}
	var result ApprovalResult
	if err := s.db.RPC(ctx, "create_human_approval", request, &result); err != nil {
		return ApprovalResult{}, err
	}
	return result, nil
}

// Resolve applies one Telegram approval decision and returns the saved purchase values.
func (s *ApprovalStore) Resolve(ctx context.Context, telegramID int64, token string, decision string) (ApprovalResolution, error) {
	if telegramID <= 0 || strings.TrimSpace(token) == "" || (decision != "approve" && decision != "reject") {
		return ApprovalResolution{}, fmt.Errorf("approval token, Telegram id, and decision are required")
	}
	payload := map[string]any{"p_token": token, "p_telegram_id": telegramID, "p_decision": decision}
	var result ApprovalResolution
	if err := s.db.RPC(ctx, "resolve_human_approval", payload, &result); err != nil {
		return ApprovalResolution{}, err
	}
	return result, nil
}

// NewApprovalRequest builds a request with a cryptographically random resume token.
func NewApprovalRequest(account Account, telegramID int64, product catalog.Product, quantity int, baseAmountPaise int64, finalAmountPaise int64, idempotencyKey string, reason string) (ApprovalRequest, error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return ApprovalRequest{}, fmt.Errorf("generate approval token: %w", err)
	}
	return ApprovalRequest{Token: hex.EncodeToString(tokenBytes[:]), AccountID: account.ID, TelegramID: telegramID, ProductID: product.ID, Quantity: quantity, BaseAmountPaise: baseAmountPaise, FinalAmountPaise: finalAmountPaise, IdempotencyKey: idempotencyKey, Reason: reason}, nil
}

func validateApprovalRequest(request ApprovalRequest) error {
	if strings.TrimSpace(request.Token) == "" || strings.TrimSpace(request.AccountID) == "" || request.TelegramID <= 0 || strings.TrimSpace(request.ProductID) == "" || request.Quantity <= 0 || request.BaseAmountPaise <= 0 || request.FinalAmountPaise < request.BaseAmountPaise || strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("approval request fields are invalid")
	}
	return nil
}
