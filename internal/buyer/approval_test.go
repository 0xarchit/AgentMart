// Tests for persisted human approval request construction.
package buyer

import (
	"testing"

	"agentmart/internal/catalog"
)

func TestNewApprovalRequestGeneratesResumeToken(t *testing.T) {
	account := Account{ID: "account"}
	product := catalog.Product{ID: "product", PricePaise: 100}
	first, err := NewApprovalRequest(account, 10, product, 1, 100, 140, "purchase-1", "human approval required")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewApprovalRequest(account, 10, product, 1, 100, 140, "purchase-2", "human approval required")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" || len(first.Token) != 32 || first.Token == second.Token {
		t.Fatalf("tokens = %q and %q", first.Token, second.Token)
	}
	if err := validateApprovalRequest(first); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalRequestRejectsDiscount(t *testing.T) {
	err := validateApprovalRequest(ApprovalRequest{Token: "token", AccountID: "account", TelegramID: 10, ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 99, IdempotencyKey: "purchase", Reason: "approval"})
	if err == nil {
		t.Fatal("expected discount rejection")
	}
}
