// Package buyer coordinates trusted purchase facts and fulfillment dependencies.
package buyer

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"agentmart/internal/gate"
	"agentmart/internal/negotiation"
	buyerreasoning "agentmart/internal/reasoning"
	"agentmart/internal/runid"
	"agentmart/internal/supabase"
)

// Account contains wallet facts used by the Gate.
type Account struct {
	ID                 string `json:"id"`
	WalletBalancePaise int64  `json:"wallet_balance_paise"`
	SpendLimitPaise    int64  `json:"spend_limit_paise"`
}

type telegramLink struct {
	AccountID string `json:"account_id"`
}

// Store reads trusted buyer state and persists Gate decisions.
type Store struct{ db *supabase.Client }

// NewStore constructs a buyer state store.
func NewStore(db *supabase.Client) *Store { return &Store{db: db} }

// AccountForTelegram resolves the wallet account linked to a Telegram identity.
func (s *Store) AccountForTelegram(ctx context.Context, telegramID int64) (Account, error) {
	linksQuery := url.Values{"select": {"account_id"}, "telegram_id": {"eq." + strconv.FormatInt(telegramID, 10)}, "limit": {"1"}}
	var links []telegramLink
	if err := s.db.Get(ctx, "telegram_links", linksQuery, &links); err != nil {
		return Account{}, err
	}
	if len(links) == 0 {
		return Account{}, fmt.Errorf("Telegram account is not linked")
	}
	accountQuery := url.Values{"select": {"id,wallet_balance_paise,spend_limit_paise"}, "id": {"eq." + links[0].AccountID}, "limit": {"1"}}
	var accounts []Account
	if err := s.db.Get(ctx, "accounts", accountQuery, &accounts); err != nil {
		return Account{}, err
	}
	if len(accounts) == 0 {
		return Account{}, fmt.Errorf("linked wallet account not found")
	}
	return accounts[0], nil
}

// insertTrail writes one trail row, tagged with the run it belongs to when the
// work is running inside one. Correlation is applied here so no caller can
// forget it.
func (s *Store) insertTrail(ctx context.Context, row map[string]any) error {
	if id := runid.From(ctx); id != "" {
		row["run_id"] = id
	}
	return s.db.Insert(ctx, "audit_log", row, nil)
}

// FundingPayments lists the captured payments that funded an allowance, oldest
// first, so a reversal draws them down in the order the money arrived.
func (s *Store) FundingPayments(ctx context.Context, accountID string) ([]FundingPayment, error) {
	query := url.Values{
		"select":              {"razorpay_payment_id,amount_paise"},
		"account_id":          {"eq." + accountID},
		"entry_type":          {"eq.topup"},
		"razorpay_payment_id": {"not.is.null"},
		"order":               {"created_at.asc"},
	}
	var rows []struct {
		PaymentID   string `json:"razorpay_payment_id"`
		AmountPaise int64  `json:"amount_paise"`
	}
	if err := s.db.Get(ctx, "wallet_ledger", query, &rows); err != nil {
		return nil, err
	}
	payments := make([]FundingPayment, 0, len(rows))
	for _, row := range rows {
		payments = append(payments, FundingPayment{PaymentID: row.PaymentID, AmountPaise: row.AmountPaise})
	}
	return payments, nil
}

// RecordReversal stores the gateway refunds a cancellation produced on the order
// and writes one trail row for them. A shortfall becomes the reason rather than
// being dropped, so an incomplete reversal is visible in the trail.
func (s *Store) RecordReversal(ctx context.Context, accountID, orderID string, result ReverseResult) error {
	if len(result.RefundIDs) > 0 {
		filter := url.Values{"id": {"eq." + orderID}}
		payload := map[string]any{"razorpay_refund_ids": result.RefundIDs}
		if err := s.db.Update(ctx, "orders", filter, payload, nil); err != nil {
			return err
		}
	}
	reason := "reversed at the payment gateway"
	if result.ShortfallPaise > 0 {
		reason = fmt.Sprintf("reversed short of the credited amount by %d paise", result.ShortfallPaise)
	}
	return s.insertTrail(ctx, map[string]any{
		"account_id": accountID,
		"order_id":   orderID,
		"actor":      "gate",
		"action":     "refund_reversed",
		"reason":     reason,
		"payload": map[string]any{
			"refund_ids":      result.RefundIDs,
			"amount_paise":    result.ReversedPaise,
			"shortfall_paise": result.ShortfallPaise,
		},
	})
}

// RecordReversalFailure records that the internal credit succeeded and the gateway
// reversal did not, because a money path that leaves no row is the one thing this
// trail is not allowed to do.
func (s *Store) RecordReversalFailure(ctx context.Context, accountID, orderID string, cause error) error {
	return s.insertTrail(ctx, map[string]any{
		"account_id": accountID,
		"order_id":   orderID,
		"actor":      "gate",
		"action":     "refund_reversal_failed",
		"reason":     "allowance credited, gateway reversal did not complete",
		"payload":    map[string]any{"cause": cause.Error()},
	})
}

// RecordPurchaseFailure writes a purchase refusal that never reached the gate, or
// reached it and could not move the money. The account is left unresolved because
// the failure may be the account lookup itself.
func (s *Store) RecordPurchaseFailure(ctx context.Context, telegramID int64, productID string, quantity int, cause error) error {
	return s.insertTrail(ctx, map[string]any{
		"actor":  "buyer_agent",
		"action": "purchase_failed",
		"reason": cause.Error(),
		"payload": map[string]any{
			"telegram_id": telegramID,
			"product_id":  productID,
			"quantity":    quantity,
		},
	})
}

// RecordRefundFailure writes a refund refusal that never reached the credit. Like
// a purchase refusal the account is left unresolved, because the failure may be
// the account lookup itself. The order id is written into the payload rather than
// the order_id column for the same reason: it is what the person asked for, not
// necessarily an order that exists.
func (s *Store) RecordRefundFailure(ctx context.Context, telegramID int64, orderID string, cause error) error {
	return s.insertTrail(ctx, map[string]any{
		"actor":  "buyer_agent",
		"action": "refund_failed",
		"reason": cause.Error(),
		"payload": map[string]any{
			"telegram_id": telegramID,
			"order_id":    orderID,
		},
	})
}

// RecordGateDecision persists every purchase approval and rejection.
func (s *Store) RecordGateDecision(ctx context.Context, decision gate.Decision) error {
	action := "gate_rejected"
	if decision.Approved {
		action = "gate_approved"
	}
	payload := map[string]any{"product_id": decision.Request.ProductID, "quantity": decision.Request.Quantity, "amount_paise": decision.Request.FinalAmountPaise}
	row := map[string]any{"account_id": decision.Request.AccountID, "actor": "gate", "action": action, "reason": decision.Reason, "payload": payload}
	return s.insertTrail(ctx, row)
}

// RecordReasoningDecision persists the buyer's bounded decision and rationale.
func (s *Store) RecordReasoningDecision(ctx context.Context, telegramID int64, input buyerreasoning.Input, decision buyerreasoning.Decision) error {
	account, err := s.AccountForTelegram(ctx, telegramID)
	if err != nil {
		return err
	}
	row := map[string]any{
		"account_id": account.ID,
		"actor":      "buyer_agent",
		"action":     "reasoning_decision",
		"reason":     decision.Rationale,
		"payload": map[string]any{
			"action": decision.Action, "product_id": decision.ProductID,
			"quantity": decision.Quantity, "max_spend_paise": decision.MaxSpendPaise,
			"input": input,
		},
	}
	return s.insertTrail(ctx, row)
}

// AgentRun is one completed buyer-graph run, recorded for explainability: what
// the agent decided, at what price, and the node trace that led there.
type AgentRun struct {
	Action      string   `json:"action"`
	ProductID   string   `json:"product_id"`
	ProductName string   `json:"product_name,omitempty"`
	Quantity    int      `json:"quantity"`
	FinalPaise  int64    `json:"final_amount_paise"`
	SessionID   string   `json:"session_id,omitempty"`
	Accepted    bool     `json:"a2a_accepted"`
	NeedsHuman  bool     `json:"needs_human"`
	Rationale   string   `json:"-"`
	Steps       []string `json:"steps,omitempty"`
	// Transcript is what the two sides actually said. It is recorded with the
	// decision because the words are the evidence for the price, and a chat
	// attachment the person can delete is not a trail.
	Transcript []negotiation.Turn `json:"transcript,omitempty"`
}

// RecordAgentRun persists the buyer graph's decision and trace.
func (s *Store) RecordAgentRun(ctx context.Context, telegramID int64, request string, run AgentRun) error {
	account, err := s.AccountForTelegram(ctx, telegramID)
	if err != nil {
		return err
	}
	return s.insertTrail(ctx, map[string]any{
		"account_id": account.ID,
		"actor":      "buyer_agent",
		"action":     "agent_run",
		"reason":     run.Rationale,
		"payload":    map[string]any{"request": request, "run": run},
	})
}

func (s *Store) RecordUpdateDeadLetter(ctx context.Context, telegramID int64, text string, cause error) error {
	account, err := s.AccountForTelegram(ctx, telegramID)
	if err != nil {
		return err
	}
	return s.insertTrail(ctx, map[string]any{
		"account_id": account.ID, "actor": "user", "action": "update_dead_letter",
		"reason": cause.Error(), "payload": map[string]any{"text": text},
	})
}
