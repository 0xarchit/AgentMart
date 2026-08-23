// Wallet refund orchestration for Telegram buyers.
package buyer

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/wallet"
)

type walletRefunder interface {
	Refund(context.Context, wallet.RefundRequest) (wallet.RefundResult, error)
}

// RefundRequest identifies a Telegram refund attempt.
type RefundRequest struct {
	TelegramID int64
	MessageID  int
	OrderID    string
	Reason     string
}

// RefundResult reports the wallet refund outcome.
type RefundResult struct {
	Approved    bool
	Duplicate   bool
	OrderID     string
	AmountPaise int64
	Reason      string
}

// RefundService coordinates account lookup and wallet-only refunds.
type RefundService struct {
	accounts accountReader
	wallet   walletRefunder
}

// NewRefundService constructs a buyer refund service.
func NewRefundService(accounts accountReader, walletService walletRefunder) *RefundService {
	return &RefundService{accounts: accounts, wallet: walletService}
}

// Refund resolves the Telegram wallet and applies one idempotent credit.
func (s *RefundService) Refund(ctx context.Context, request RefundRequest) (RefundResult, error) {
	if request.TelegramID <= 0 || request.MessageID <= 0 || strings.TrimSpace(request.OrderID) == "" || strings.TrimSpace(request.Reason) == "" {
		return RefundResult{}, fmt.Errorf("Telegram id, message id, order id, and reason are required")
	}
	account, err := s.accounts.AccountForTelegram(ctx, request.TelegramID)
	if err != nil {
		return RefundResult{}, err
	}
	result, err := s.wallet.Refund(ctx, wallet.RefundRequest{
		AccountID:      account.ID,
		OrderID:        strings.TrimSpace(request.OrderID),
		Reason:         strings.TrimSpace(request.Reason),
		IdempotencyKey: fmt.Sprintf("telegram:%d:refund:%d", request.TelegramID, request.MessageID),
	})
	if err != nil {
		return RefundResult{}, err
	}
	return RefundResult{Approved: result.Approved, Duplicate: result.Duplicate, OrderID: result.OrderID, AmountPaise: result.AmountPaise, Reason: result.Reason}, nil
}
