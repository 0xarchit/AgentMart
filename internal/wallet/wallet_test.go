// Tests for wallet request validation.
package wallet

import "testing"

func TestFulfillRejectsDiscountBelowProposal(t *testing.T) {
	err := (FulfillRequest{AccountID: "a", ProductID: "p", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 99, RazorpayOrderID: "r", IdempotencyKey: "k", RefundWindowMinutes: 60}).validate()
	if err == nil {
		t.Fatal("expected final amount validation error")
	}
}

func TestTopUpRequiresVerifiedPaymentIDs(t *testing.T) {
	err := (TopUpRequest{AccountID: "a", AmountPaise: 100, IdempotencyKey: "k"}).validate()
	if err == nil {
		t.Fatal("expected payment id validation error")
	}
}
