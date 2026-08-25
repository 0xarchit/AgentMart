// Package buyer coordinates trusted purchase facts and fulfillment dependencies.
package buyer

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"agentmart/internal/gate"
	buyerreasoning "agentmart/internal/reasoning"
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

// RecordGateDecision persists every purchase approval and rejection.
func (s *Store) RecordGateDecision(ctx context.Context, decision gate.Decision) error {
	action := "gate_rejected"
	if decision.Approved {
		action = "gate_approved"
	}
	payload := map[string]any{"product_id": decision.Request.ProductID, "quantity": decision.Request.Quantity, "amount_paise": decision.Request.FinalAmountPaise}
	row := map[string]any{"account_id": decision.Request.AccountID, "actor": "gate", "action": action, "reason": decision.Reason, "payload": payload}
	return s.db.Insert(ctx, "audit_log", row, nil)
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
	return s.db.Insert(ctx, "audit_log", row, nil)
}

func (s *Store) RecordUpdateDeadLetter(ctx context.Context, telegramID int64, text string, cause error) error {
	account, err := s.AccountForTelegram(ctx, telegramID)
	if err != nil {
		return err
	}
	return s.db.Insert(ctx, "audit_log", map[string]any{
		"account_id": account.ID, "actor": "user", "action": "update_dead_letter",
		"reason": cause.Error(), "payload": map[string]any{"text": text},
	}, nil)
}
