// Package buyer coordinates trusted purchase facts and fulfillment dependencies.
package buyer

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"agentmart/internal/gate"
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
