// Tests for buyer command response selection.
package main

import (
	"context"
	"errors"
	"testing"

	"agentmart/internal/buyer"
)

type fakeLinker struct{ err error }

func (f fakeLinker) Redeem(context.Context, string, int64) (string, error) {
	return "account", f.err
}

type fakePurchaser struct {
	result buyer.PurchaseResult
	err    error
}

func (f fakePurchaser) Purchase(context.Context, buyer.PurchaseRequest) (buyer.PurchaseResult, error) {
	return f.result, f.err
}

func TestResponseForCommand(t *testing.T) {
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, 1, 1, []string{"/buy"}); got == "" {
		t.Fatal("expected purchase response")
	}
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, 1, 1, []string{"/unknown"}); got != "Use /start, /link TOKEN, /buy, /approve TOKEN, or /reject TOKEN." {
		t.Fatalf("unexpected fallback response: %q", got)
	}
}

func TestLinkCommand(t *testing.T) {
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, 10, 1, []string{"/link", "token"})
	if got != "Telegram is now linked to your AgentMart wallet." {
		t.Fatalf("response = %q", got)
	}
	got, _ = responseForCommand(t.Context(), fakeLinker{err: errors.New("expired")}, fakePurchaser{}, 10, 1, []string{"/link", "token"})
	if got != "That link token is invalid, expired, or already used." {
		t.Fatalf("response = %q", got)
	}
}

func TestBuyCommand(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 45000, RazorpayOrderID: "order"}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, 10, 5, []string{"/buy", "product", "1"})
	if got != "Purchase fulfilled via wallet for INR 450.00. Audit order: order" {
		t.Fatalf("response = %q", got)
	}
}

func TestBuyCommandApproval(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{ApprovalRequired: true, ApprovalToken: "token", AmountPaise: 60000}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, 10, 5, []string{"/buy", "product", "1"})
	if got != "Human approval required for INR 600.00. Approval token: token" {
		t.Fatalf("response = %q", got)
	}
}
