// Package linking connects Telegram identities to wallet accounts.
package linking

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/supabase"
)

// Service redeems trusted single-use dashboard tokens.
type Service struct{ db *supabase.Client }

// NewService constructs an account-linking service.
func NewService(db *supabase.Client) *Service { return &Service{db: db} }

// Redeem connects a Telegram user to the account owning the token.
func (s *Service) Redeem(ctx context.Context, token string, telegramID int64) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || telegramID <= 0 {
		return "", fmt.Errorf("token and Telegram id are required")
	}
	var accountID string
	if err := s.db.RPC(ctx, "redeem_telegram_link", map[string]any{"p_token": token, "p_telegram_id": telegramID}, &accountID); err != nil {
		return "", fmt.Errorf("redeem Telegram link: %w", err)
	}
	if accountID == "" {
		return "", fmt.Errorf("link returned no account")
	}
	return accountID, nil
}
