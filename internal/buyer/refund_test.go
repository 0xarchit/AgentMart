// Tests for Telegram wallet refund orchestration.
package buyer

import (
	"context"
	"testing"

	"agentmart/internal/wallet"
)

type fakeRefundAccounts struct{}

func (fakeRefundAccounts) AccountForTelegram(context.Context, int64) (Account, error) {
	return Account{ID: "account"}, nil
}

type fakeRefunder struct{ request wallet.RefundRequest }

func (f *fakeRefunder) Refund(_ context.Context, request wallet.RefundRequest) (wallet.RefundResult, error) {
	f.request = request
	return wallet.RefundResult{Approved: true, OrderID: request.OrderID, AmountPaise: 1250}, nil
}

func TestRefundDerivesStableIdempotencyKey(t *testing.T) {
	refunder := &fakeRefunder{}
	service := NewRefundService(fakeRefundAccounts{}, refunder)
	result, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order", Reason: "changed mind"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved || result.AmountPaise != 1250 {
		t.Fatalf("result = %+v", result)
	}
	if refunder.request.IdempotencyKey != "telegram:7:refund:9" {
		t.Fatalf("idempotency key = %q", refunder.request.IdempotencyKey)
	}
}

func TestRefundRequiresReason(t *testing.T) {
	service := NewRefundService(fakeRefundAccounts{}, &fakeRefunder{})
	if _, err := service.Refund(t.Context(), RefundRequest{TelegramID: 7, MessageID: 9, OrderID: "order"}); err == nil {
		t.Fatal("expected validation error")
	}
}
