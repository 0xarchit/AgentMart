// Tests for Telegram link request validation.
package linking

import "testing"

func TestRedeemRejectsMissingInput(t *testing.T) {
	service := NewService(nil)
	if _, err := service.Redeem(t.Context(), "", 1); err == nil {
		t.Fatal("expected token validation error")
	}
	if _, err := service.Redeem(t.Context(), "token", 0); err == nil {
		t.Fatal("expected Telegram id validation error")
	}
}
