// Wallet refund orchestration for Telegram buyers.
package buyer

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/runid"
	"agentmart/internal/wallet"
)

type walletRefunder interface {
	Refund(context.Context, wallet.RefundRequest) (wallet.RefundResult, error)
}

// refundRecorder writes what happened on the refund path: what the gateway did,
// what it did not do, and the refusals that stopped the refund before either. It
// also holds the reversal a credit is still owed, so one that was interrupted can
// be finished later instead of being lost or repeated.
type refundRecorder interface {
	OutstandingReversal(context.Context, string, string) (ReversalAttempt, bool, error)
	SettleReversal(context.Context, string) error
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

// refundKey is what one refund is idempotent on: one message asking for one
// credit. It is derived from the message rather than the order so that asking
// twice cannot credit twice, and it is derived here rather than in two places
// because the gateway reversal has to present the same key the credit used.
func refundKey(request RefundRequest) string {
	return fmt.Sprintf("telegram:%d:refund:%d", request.TelegramID, request.MessageID)
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
// allowed to go missing quietly. A refusal can also be the moment a reversal an
// earlier attempt left unfinished is finished, so the gateway side is settled here
// even when this call had no credit of its own to make.
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
		IdempotencyKey: refundKey(request),
	})
	if err != nil {
		return RefundResult{}, s.recordFailure(ctx, request.TelegramID, strings.TrimSpace(request.OrderID), err)
	}
	outcome := RefundResult{Approved: result.Approved, Duplicate: result.Duplicate, OrderID: result.OrderID, AmountPaise: result.AmountPaise, Reason: result.Reason}
	if s.reversal == nil {
		return outcome, nil
	}
	reverse, owed, err := s.owedReversal(ctx, account.ID, request, result)
	if err != nil {
		// Not knowing whether a reversal is owed is not something to answer the
		// person with: their credit is unaffected either way. It leaves a row.
		if s.recorder != nil {
			if recordErr := s.recorder.RecordReversalFailure(ctx, account.ID, strings.TrimSpace(request.OrderID), err); recordErr != nil {
				return outcome, recordErr
			}
		}
		return outcome, nil
	}
	if !owed {
		return outcome, nil
	}

	// The allowance is already credited, so a gateway refusal must not fail the
	// refund the person was promised. It is recorded and the credit stands.
	reversed, reverseErr := s.reversal.Reverse(ctx, reverse)
	if reverseErr != nil && s.recorder != nil {
		if err := s.recorder.RecordReversalFailure(ctx, account.ID, reverse.OrderID, reverseErr); err != nil {
			return outcome, err
		}
	}
	if len(reversed.RefundIDs) > 0 || reversed.ShortfallPaise > 0 {
		if s.recorder != nil {
			if err := s.recorder.RecordReversal(ctx, account.ID, reverse.OrderID, reversed); err != nil {
				return outcome, err
			}
		}
		outcome.RefundIDs = reversed.RefundIDs
		outcome.ShortfallPaise = reversed.ShortfallPaise
	}
	if reverseErr == nil && s.recorder != nil {
		// The gateway has answered for all of it. A shortfall is an answer too:
		// the funding payments have nothing left to give, so asking again would
		// reverse nothing and the trail already says so.
		if err := s.recorder.SettleReversal(ctx, reverse.OrderID); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// owedReversal decides which gateway reversal this refund still owes, if any.
//
// A fresh credit owes the reversal of what it just credited. Anything else owes
// only what an interrupted attempt left behind, and that is read back with the
// inputs the first attempt used rather than rebuilt from the request in front of
// us: the gateway hashes those inputs into the request, so a second /refund
// worded differently, or minted in a later run, would arrive as a different
// request under a key already used to send money back.
func (s *RefundService) owedReversal(ctx context.Context, accountID string, request RefundRequest, result wallet.RefundResult) (ReverseRequest, bool, error) {
	if result.Approved && !result.Duplicate {
		return ReverseRequest{
			AccountID:      accountID,
			OrderID:        result.OrderID,
			AmountPaise:    result.AmountPaise,
			IdempotencyKey: refundKey(request),
			Reason:         strings.TrimSpace(request.Reason),
			RunID:          runid.From(ctx),
		}, true, nil
	}
	if s.recorder == nil {
		return ReverseRequest{}, false, nil
	}
	// The order asked for, not the one the wallet answered with, because a refusal
	// answers with none and a refusal is the ordinary way a resume arrives: the
	// second /refund is a different message, so there is no duplicate credit to
	// recognise and the wallet refuses an order it has already refunded.
	attempt, found, err := s.recorder.OutstandingReversal(ctx, accountID, strings.TrimSpace(request.OrderID))
	if err != nil || !found {
		return ReverseRequest{}, false, err
	}
	return ReverseRequest{
		AccountID:      accountID,
		OrderID:        attempt.OrderID,
		AmountPaise:    attempt.AmountPaise,
		IdempotencyKey: attempt.IdempotencyKey,
		Reason:         attempt.Reason,
		RunID:          attempt.RunID,
	}, true, nil
}
