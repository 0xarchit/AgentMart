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

// refundRecorder writes what happened on the refund path: what the gateway did,
// what it did not do, and the refusals that stopped the refund before either.
type refundRecorder interface {
	RecordReversal(context.Context, string, string, ReverseResult) error
	RecordReversalFailure(context.Context, string, string, error) error
	RecordRefundFailure(context.Context, int64, string, error) error
}

// RefundRequest identifies a Telegram refund attempt.
type RefundRequest struct {
	TelegramID int64
	MessageID  int
	OrderID    string
	Reason     string
}

// RefundResult reports the wallet refund outcome, plus what the gateway confirmed
// when a reversal path is configured.
type RefundResult struct {
	Approved       bool
	Duplicate      bool
	OrderID        string
	AmountPaise    int64
	Reason         string
	RefundIDs      []string
	ShortfallPaise int64
}

// RefundService coordinates account lookup, the internal credit, and the gateway
// reversal that leaves evidence for it.
type RefundService struct {
	accounts accountReader
	wallet   walletRefunder
	reversal Reversal
	recorder refundRecorder
}

// NewRefundService constructs a buyer refund service.
func NewRefundService(accounts accountReader, walletService walletRefunder) *RefundService {
	return &RefundService{accounts: accounts, wallet: walletService}
}

// UseReversal attaches the gateway reversal and the trail it writes to. Without it
// the allowance credit is the whole refund, which is how this behaved before.
func (s *RefundService) UseReversal(reversal Reversal, recorder refundRecorder) {
	s.reversal = reversal
	s.recorder = recorder
}

// recordFailure writes one refund refusal to the trail and hands back the refusal.
// A trail write that itself fails is folded into it rather than dropped.
func (s *RefundService) recordFailure(ctx context.Context, telegramID int64, orderID string, cause error) error {
	if cause == nil || s.recorder == nil {
		return cause
	}
	if recordErr := s.recorder.RecordRefundFailure(ctx, telegramID, orderID, cause); recordErr != nil {
		return fmt.Errorf("%w (recording it also failed: %v)", cause, recordErr)
	}
	return cause
}

// Refund resolves the Telegram wallet and applies one idempotent credit. Every
// refusal on the way leaves a row: the money the person was promised back is not
// allowed to go missing quietly.
func (s *RefundService) Refund(ctx context.Context, request RefundRequest) (RefundResult, error) {
	if request.TelegramID <= 0 || request.MessageID <= 0 || strings.TrimSpace(request.OrderID) == "" || strings.TrimSpace(request.Reason) == "" {
		return RefundResult{}, s.recordFailure(ctx, request.TelegramID, strings.TrimSpace(request.OrderID), fmt.Errorf("Telegram id, message id, order id, and reason are required"))
	}
	account, err := s.accounts.AccountForTelegram(ctx, request.TelegramID)
	if err != nil {
		return RefundResult{}, s.recordFailure(ctx, request.TelegramID, strings.TrimSpace(request.OrderID), err)
	}
	result, err := s.wallet.Refund(ctx, wallet.RefundRequest{
		AccountID:      account.ID,
		OrderID:        strings.TrimSpace(request.OrderID),
		Reason:         strings.TrimSpace(request.Reason),
		IdempotencyKey: fmt.Sprintf("telegram:%d:refund:%d", request.TelegramID, request.MessageID),
	})
	if err != nil {
		return RefundResult{}, s.recordFailure(ctx, request.TelegramID, strings.TrimSpace(request.OrderID), err)
	}
	outcome := RefundResult{Approved: result.Approved, Duplicate: result.Duplicate, OrderID: result.OrderID, AmountPaise: result.AmountPaise, Reason: result.Reason}
	if !result.Approved || result.Duplicate || s.reversal == nil {
		return outcome, nil
	}

	// The allowance is already credited, so a gateway refusal must not fail the
	// refund the person was promised. It is recorded and the credit stands.
	reversed, reverseErr := s.reversal.Reverse(ctx, ReverseRequest{
		AccountID:      account.ID,
		OrderID:        result.OrderID,
		AmountPaise:    result.AmountPaise,
		IdempotencyKey: fmt.Sprintf("telegram:%d:refund:%d", request.TelegramID, request.MessageID),
		Reason:         strings.TrimSpace(request.Reason),
	})
	if reverseErr != nil && s.recorder != nil {
		if err := s.recorder.RecordReversalFailure(ctx, account.ID, result.OrderID, reverseErr); err != nil {
			return outcome, err
		}
	}
	if len(reversed.RefundIDs) > 0 || reversed.ShortfallPaise > 0 {
		if s.recorder != nil {
			if err := s.recorder.RecordReversal(ctx, account.ID, result.OrderID, reversed); err != nil {
				return outcome, err
			}
		}
		outcome.RefundIDs = reversed.RefundIDs
		outcome.ShortfallPaise = reversed.ShortfallPaise
	}
	return outcome, nil
}
