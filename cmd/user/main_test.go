// Tests for buyer command response selection.
package main

import (
	"context"
	"errors"
	"testing"

	"agentmart/internal/buyer"
	"agentmart/internal/negotiationclient"
	buyerreasoning "agentmart/internal/reasoning"
)

type fakeLinker struct{ err error }

func (f fakeLinker) Redeem(context.Context, string, int64) (string, error) {
	return "account", f.err
}

type fakePurchaser struct {
	result buyer.PurchaseResult
	err    error
}

type fakeRefunder struct {
	result buyer.RefundResult
	err    error
}

type fakeNegotiator struct{}

type fakeDecisionMaker struct {
	decision buyerreasoning.Decision
}

func (f fakeDecisionMaker) Decide(context.Context, buyerreasoning.Input) (buyerreasoning.Decision, error) {
	return f.decision, nil
}

func (fakeNegotiator) Propose(context.Context, string, int) (negotiationclient.Proposal, error) {
	return negotiationclient.Proposal{SessionID: "session", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140}, nil
}

func (fakeNegotiator) Accept(context.Context, string) (negotiationclient.Resolution, error) {
	return negotiationclient.Resolution{SessionID: "session", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, Status: "accepted"}, nil
}

func (fakeNegotiator) Decline(context.Context, string, string) (negotiationclient.Resolution, error) {
	return negotiationclient.Resolution{SessionID: "session", Status: "declined"}, nil
}

func (f fakeRefunder) Refund(context.Context, buyer.RefundRequest) (buyer.RefundResult, error) {
	return f.result, f.err
}

func (f fakePurchaser) Purchase(context.Context, buyer.PurchaseRequest) (buyer.PurchaseResult, error) {
	return f.result, f.err
}

func TestResponseForCommand(t *testing.T) {
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 1, 1, []string{"/buy"}); got == "" {
		t.Fatal("expected purchase response")
	}
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 1, 1, []string{"/unknown"}); got != "Use /start, /link TOKEN, /buy, /negotiate, /accept, /decline, /approve TOKEN, /reject TOKEN, or /refund ORDER_ID REASON." {
		t.Fatalf("unexpected fallback response: %q", got)
	}
}

func TestLinkCommand(t *testing.T) {
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 10, 1, []string{"/link", "token"})
	if got != "Telegram is now linked to your AgentMart wallet." {
		t.Fatalf("response = %q", got)
	}
	got, _ = responseForCommand(t.Context(), fakeLinker{err: errors.New("expired")}, fakePurchaser{}, fakeRefunder{}, 10, 1, []string{"/link", "token"})
	if got != "That link token is invalid, expired, or already used." {
		t.Fatalf("response = %q", got)
	}
}

func TestBuyCommand(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 45000, RazorpayOrderID: "order"}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/buy", "product", "1"})
	if got != "Purchase fulfilled via wallet for INR 450.00. Audit order: order" {
		t.Fatalf("response = %q", got)
	}
}

func TestBuyCommandApproval(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{ApprovalRequired: true, ApprovalToken: "token", AmountPaise: 60000}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/buy", "product", "1"})
	if got != "Human approval required for INR 600.00. Approval token: token" {
		t.Fatalf("response = %q", got)
	}
}

func TestNegotiationCommands(t *testing.T) {
	negotiator := fakeNegotiator{}
	proposal, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 10, 5, []string{"/negotiate", "product", "1"}, negotiator)
	if proposal != "Merchant counter offer: INR 1.40 for 1 unit(s). Session: session. Use /accept session or /decline session." {
		t.Fatalf("proposal = %q", proposal)
	}
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 140, RazorpayOrderID: "order"}}
	accepted, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/accept", "session"}, negotiator)
	if accepted != "Negotiated purchase fulfilled via wallet for INR 1.40. Audit order: order" {
		t.Fatalf("accepted = %q", accepted)
	}
}

func TestShopCommandUsesReasoningBeforePurchase(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 140, RazorpayOrderID: "order"}}
	services := commandServices{negotiations: fakeNegotiator{}, reasoning: fakeDecisionMaker{decision: buyerreasoning.Decision{Action: buyerreasoning.ActionBuy}}}
	got, err := responseForCommandWithServices(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/shop", "product", "1", "200"}, services)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Reasoned purchase fulfilled via wallet for INR 1.40. Audit order: order" {
		t.Fatalf("response = %q", got)
	}
}

func TestRefundCommand(t *testing.T) {
	refund := fakeRefunder{result: buyer.RefundResult{Approved: true, OrderID: "order", AmountPaise: 1250}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, refund, 10, 5, []string{"/refund", "order", "changed", "mind"})
	if got != "Refund approved via wallet for INR 12.50. Order: order" {
		t.Fatalf("response = %q", got)
	}
}

func TestRefundCommandDuplicate(t *testing.T) {
	refund := fakeRefunder{result: buyer.RefundResult{Duplicate: true, OrderID: "order"}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, refund, 10, 5, []string{"/refund", "order", "changed", "mind"})
	if got != "Refund already applied for order order." {
		t.Fatalf("response = %q", got)
	}
}

func TestRefundCommandRejected(t *testing.T) {
	refund := fakeRefunder{result: buyer.RefundResult{Reason: "refund window has expired"}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, refund, 10, 5, []string{"/refund", "order", "changed", "mind"})
	if got != "Refund rejected: refund window has expired" {
		t.Fatalf("response = %q", got)
	}
}
