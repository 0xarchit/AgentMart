// Tests for buyer command response selection.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/buyer"
	"agentmart/internal/catalog"
	"agentmart/internal/negotiationclient"
	buyerreasoning "agentmart/internal/reasoning"
	"agentmart/internal/shopgraph"
	"agentmart/internal/telegram"
)

type fakeLinker struct{ err error }

func (f fakeLinker) Redeem(context.Context, string, int64) (string, error) {
	return "account", f.err
}

type fakePurchaser struct {
	result buyer.PurchaseResult
	err    error
}

func (f fakePurchaser) ResolveApproval(context.Context, int64, string, string) (buyer.PurchaseResult, error) {
	return f.result, f.err
}

func (f fakePurchaser) RequestApproval(context.Context, buyer.PurchaseRequest, string) (buyer.PurchaseResult, error) {
	return buyer.PurchaseResult{ApprovalRequired: true, ApprovalToken: "token", AmountPaise: f.result.AmountPaise}, f.err
}

type fakeRefunder struct {
	result buyer.RefundResult
	err    error
}

type fakeNegotiator struct{}

type fakeDecisionMaker struct {
	decision buyerreasoning.Decision
}

type fakeReasoningAuditor struct {
	input    buyerreasoning.Input
	decision buyerreasoning.Decision
	runs     []buyer.AgentRun
}

type fakeAccountFacts struct{}

func (fakeAccountFacts) AccountForTelegram(context.Context, int64) (buyer.Account, error) {
	return buyer.Account{WalletBalancePaise: 500, SpendLimitPaise: 200}, nil
}

type fakeProductFacts struct{}

func (fakeProductFacts) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "product", Name: "Trusted Product", PricePaise: 100, Stock: 4, WarrantyYears: 3, TrustScore: 92}, nil
}

func (f *fakeReasoningAuditor) RecordReasoningDecision(_ context.Context, _ int64, input buyerreasoning.Input, decision buyerreasoning.Decision) error {
	f.input = input
	f.decision = decision
	return nil
}

func (f *fakeReasoningAuditor) RecordAgentRun(_ context.Context, _ int64, _ string, run buyer.AgentRun) error {
	f.runs = append(f.runs, run)
	return nil
}

func (f fakeDecisionMaker) Decide(context.Context, buyerreasoning.Input) (buyerreasoning.Decision, error) {
	return f.decision, nil
}

func (fakeNegotiator) Browse(context.Context, string, int64, string) (negotiationclient.Shortlist, error) {
	return negotiationclient.Shortlist{
		Greeting: "welcome in",
		Options: []negotiationclient.ShortlistOption{{
			ProductID: "product", Name: "Trimmer", PricePaise: 100, Pitch: "solid everyday pick",
		}},
	}, nil
}

func (fakeNegotiator) Propose(context.Context, string, int) (negotiationclient.Proposal, error) {
	return negotiationclient.Proposal{SessionID: "session", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, Reason: "three-year warranty"}, nil
}

func (f fakeNegotiator) ProposeAs(ctx context.Context, productID string, quantity int, _ string) (negotiationclient.Proposal, error) {
	return f.Propose(ctx, productID, quantity)
}

func (fakeNegotiator) Accept(context.Context, string) (negotiationclient.Resolution, error) {
	return negotiationclient.Resolution{SessionID: "session", ProductID: "product", Quantity: 1, BaseAmountPaise: 100, FinalAmountPaise: 140, Status: "accepted"}, nil
}

func (fakeNegotiator) Decline(context.Context, string, string) (negotiationclient.Resolution, error) {
	return negotiationclient.Resolution{SessionID: "session", Status: "declined"}, nil
}

func (fakeNegotiator) Counter(context.Context, string, int64) (negotiationclient.Resolution, error) {
	return negotiationclient.Resolution{SessionID: "session", Status: "accepted", FinalAmountPaise: 100}, nil
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
	// An unknown command should point at the thing that actually works rather than
	// list every command at someone who has just mistyped one.
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 1, 1, []string{"/unknown"})
	for _, want := range []string{"did not recognise", "buy me a trimmer", "/start"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the fallback is missing %q: %q", want, got)
		}
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
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 45000, OrderID: "order"}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/buy", "product", "1"})
	if got != "Purchase fulfilled via wallet for INR 450.00. Order: order" {
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

func TestHandleMessageApprovalResume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()
	client, err := telegram.NewClient("token", &http.Client{Transport: rewriteTelegramTransport{base: server.URL, next: server.Client().Transport}})
	if err != nil {
		t.Fatal(err)
	}
	message := &telegram.Message{MessageID: 7, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "/approve approval-token"}
	err = handleMessage(t.Context(), client, fakeLinker{}, fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 1250, OrderID: "order"}}, fakeRefunder{}, commandServices{}, message)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplyMarkupForApprovalAndOrder(t *testing.T) {
	approval := replyMarkupForResponse("Human approval required for INR 600.00. Approval token: token")
	if approval == nil || approval.InlineKeyboard[0][0].CallbackData != "/approve token" || approval.InlineKeyboard[0][1].CallbackData != "/reject token" {
		t.Fatalf("approval markup = %#v", approval)
	}
	order := replyMarkupForResponse("Purchase fulfilled via wallet for INR 12.50. Order: order-1")
	if order == nil || order.InlineKeyboard[0][0].CallbackData != "/refund order-1 Cancelled by user" {
		t.Fatalf("order markup = %#v", order)
	}
}

func TestCancelMarkupCarriesTheOrderID(t *testing.T) {
	if markup := cancelMarkup("  "); markup != nil {
		t.Fatalf("blank order id should offer no button, got %#v", markup)
	}
	markup := cancelMarkup("11111111-2222-3333-4444-555555555555")
	if markup == nil || markup.InlineKeyboard[0][0].CallbackData != "/refund 11111111-2222-3333-4444-555555555555 Cancelled by user" {
		t.Fatalf("cancel markup = %#v", markup)
	}
}

type rewriteTelegramTransport struct {
	base string
	next http.RoundTripper
}

func (t rewriteTelegramTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(t.base, "http://")
	r2 := r.Clone(r.Context())
	r2.URL = &u
	return t.next.RoundTrip(r2)
}

func TestNegotiationCommands(t *testing.T) {
	negotiator := fakeNegotiator{}
	proposal, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, fakeRefunder{}, 10, 5, []string{"/negotiate", "product", "1"}, negotiator)
	if proposal != "Merchant counter offer: INR 1.40 for 1 unit(s). Reason: three-year warranty. Session: session. Use /accept session or /decline session." {
		t.Fatalf("proposal = %q", proposal)
	}
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 140, OrderID: "order"}}
	accepted, _ := responseForCommand(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/accept", "session"}, negotiator)
	if accepted != "Negotiated purchase fulfilled via wallet for INR 1.40. Order: order" {
		t.Fatalf("accepted = %q", accepted)
	}
}

func TestShopCommandUsesReasoningBeforePurchase(t *testing.T) {
	purchase := fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 140, OrderID: "order"}}
	auditor := &fakeReasoningAuditor{}
	services := commandServices{negotiations: fakeNegotiator{}, reasoning: fakeDecisionMaker{decision: buyerreasoning.Decision{Action: buyerreasoning.ActionBuy, Rationale: "within budget"}}, audit: auditor, accounts: fakeAccountFacts{}, catalog: fakeProductFacts{}}
	got, err := responseForCommandWithServices(t.Context(), fakeLinker{}, purchase, fakeRefunder{}, 10, 5, []string{"/shop", "product", "1", "200"}, services)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Reasoned purchase fulfilled via wallet for INR 1.40. Decision: within budget. Order: order" {
		t.Fatalf("response = %q", got)
	}
	if auditor.input.BaseAmountPaise != 100 || auditor.input.FinalAmountPaise != 140 || auditor.input.PricePaise != 100 || auditor.input.TotalPaise != 140 || auditor.input.WalletPaise != 500 || auditor.input.SpendLimitPaise != 200 || auditor.input.TrustScore != 92 || auditor.decision.Rationale != "within budget" {
		t.Fatalf("audit input = %+v, decision = %+v", auditor.input, auditor.decision)
	}
}

func TestRefundCommand(t *testing.T) {
	refund := fakeRefunder{result: buyer.RefundResult{Approved: true, OrderID: "order", AmountPaise: 1250}}
	got, _ := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, refund, 10, 5, []string{"/refund", "order", "changed", "mind"})
	if got != "Refund approved via wallet for INR 12.50. Order: order" {
		t.Fatalf("response = %q", got)
	}
}

// TestASecondTapOnCancelSaysTheRefundWasAlreadyApplied locks the wording the
// person sees when the money layer reports a repeated refund.
func TestASecondTapOnCancelSaysTheRefundWasAlreadyApplied(t *testing.T) {
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

// fakeOpenDecision answers with a fixed open decision or a refusal.
type fakeOpenDecision struct {
	pending buyer.PendingApproval
	open    bool
	err     error
	asked   int
}

// PendingFor implements openDecisionReader.
func (f *fakeOpenDecision) PendingFor(context.Context, int64) (buyer.PendingApproval, bool, error) {
	f.asked++
	return f.pending, f.open, f.err
}

// telegramCalls records what the bot sent, so a test can assert the person got
// the open question back rather than a new quote.
type telegramCalls struct {
	paths  []string
	bodies []string
}

// botRecording returns a client whose calls land in the returned record.
func botRecording(t *testing.T) (*telegram.Client, *telegramCalls) {
	t.Helper()
	record := &telegramCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		record.paths = append(record.paths, r.URL.Path)
		record.bodies = append(record.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegram.NewClient("token", &http.Client{Transport: rewriteTelegramTransport{base: server.URL, next: server.Client().Transport}})
	if err != nil {
		t.Fatal(err)
	}
	return client, record
}

func TestFreeTextWithADecisionOpenAnswersItInsteadOfShoppingAgain(t *testing.T) {
	client, record := botRecording(t)
	decisions := &fakeOpenDecision{open: true, pending: buyer.PendingApproval{
		Token: "tok-9", ProductID: "trim-9", Quantity: 1,
		FinalAmountPaise: 314920, Reason: "over the standing limit",
	}}
	// A real graph service that would error if it were reached, so a regression
	// shows up as a failed run rather than as a silent pass.
	services := commandServices{
		loop: &shopgraph.Service{}, accounts: escalatingAccounts{}, catalog: stockCatalog{},
		negotiations: fakeNegotiator{}, approvals: decisions,
	}
	message := &telegram.Message{MessageID: 8, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "actually make it cheaper"}

	if err := handleMessage(t.Context(), client, fakeLinker{}, fakePurchaser{}, fakeRefunder{}, services, message); err != nil {
		t.Fatal(err)
	}
	if decisions.asked != 1 {
		t.Fatalf("the open decision was looked up %d times, want once", decisions.asked)
	}
	if len(record.bodies) != 1 {
		t.Fatalf("sent %d messages, want one: %v", len(record.bodies), record.bodies)
	}
	sent := record.bodies[0]
	// The same token and the same buttons, and the rail said out loud.
	for _, want := range []string{"tok-9", "/approve", "cannot approve a spend", "3149.20"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the reminder is missing %q: %s", want, sent)
		}
	}
	if strings.Contains(sent, "Working on it") {
		t.Fatalf("a new shopping run was started while a decision was open: %s", sent)
	}
}

func TestFreeTextShopsWhenNoDecisionIsOpen(t *testing.T) {
	client, record := botRecording(t)
	decisions := &fakeOpenDecision{open: false}
	services := commandServices{
		loop: &shopgraph.Service{}, accounts: escalatingAccounts{}, catalog: stockCatalog{},
		negotiations: fakeNegotiator{}, approvals: decisions,
	}
	message := &telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "buy me a trimmer"}

	// The graph is not built, so this reports a failure. What matters is that the
	// run was attempted at all: nothing was short circuited.
	_ = handleMessage(t.Context(), client, fakeLinker{}, fakePurchaser{}, fakeRefunder{}, services, message)
	if decisions.asked != 1 {
		t.Fatalf("the open decision was looked up %d times, want once", decisions.asked)
	}
	joined := strings.Join(record.bodies, " ")
	if !strings.Contains(joined, "Working on it") {
		t.Fatalf("no shopping run was started: %v", record.bodies)
	}
}

func TestAFailedDecisionLookupDoesNotBlockShopping(t *testing.T) {
	client, record := botRecording(t)
	decisions := &fakeOpenDecision{err: errors.New("records are unreachable")}
	services := commandServices{
		loop: &shopgraph.Service{}, accounts: escalatingAccounts{}, catalog: stockCatalog{},
		negotiations: fakeNegotiator{}, approvals: decisions,
	}
	message := &telegram.Message{MessageID: 10, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "buy me a trimmer"}

	_ = handleMessage(t.Context(), client, fakeLinker{}, fakePurchaser{}, fakeRefunder{}, services, message)
	// A read failure falls through to the behaviour that existed before the lookup
	// did, rather than refusing to shop.
	if joined := strings.Join(record.bodies, " "); !strings.Contains(joined, "Working on it") {
		t.Fatalf("a failed lookup blocked shopping: %v", record.bodies)
	}
}

// The buttons are derived from the message this file composed, so this drives the
// real command paths rather than hand written strings. A reworded reply that
// silently drops a button fails here instead of in front of a person.
func TestEveryReplyThatNeedsButtonsGetsThem(t *testing.T) {
	approval := fakePurchaser{result: buyer.PurchaseResult{
		ApprovalRequired: true, ApprovalToken: "tok-77", AmountPaise: 314920,
	}}
	fulfilled := fakePurchaser{result: buyer.PurchaseResult{
		Fulfilled: true, OrderID: "order-55", AmountPaise: 1250,
	}}
	refunded := fakeRefunder{result: buyer.RefundResult{
		Approved: true, OrderID: "order-55", AmountPaise: 1250,
	}}

	cases := []struct {
		name    string
		buy     purchaser
		refund  refunder
		command []string
		want    string
	}{
		{"approval needed", approval, fakeRefunder{}, []string{"/buy", "product", "1"}, "/approve tok-77"},
		{"purchase fulfilled", fulfilled, fakeRefunder{}, []string{"/buy", "product", "1"}, "/refund order-55 Cancelled by user"},
		{"approval resolved", fulfilled, fakeRefunder{}, []string{"/approve", "tok-77"}, "/refund order-55 Cancelled by user"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			response, err := responseForCommand(t.Context(), fakeLinker{}, one.buy, one.refund, 10, 5, one.command)
			if err != nil {
				t.Fatal(err)
			}
			markup := replyMarkupForResponse(response)
			if markup == nil {
				t.Fatalf("no buttons for %q", response)
			}
			if got := markup.InlineKeyboard[0][0].CallbackData; got != one.want {
				t.Fatalf("button = %q, want %q, from %q", got, one.want, response)
			}
		})
	}

	// An order already sent back is not one to offer sending back again.
	response, err := responseForCommand(t.Context(), fakeLinker{}, fakePurchaser{}, refunded, 10, 5,
		[]string{"/refund", "order-55", "changed", "mind"})
	if err != nil {
		t.Fatal(err)
	}
	if markup := replyMarkupForResponse(response); markup != nil {
		t.Fatalf("a refunded order was offered a refund button: %q gave %#v", response, markup)
	}
}

func TestAnApprovalTokenSurvivesExtraLines(t *testing.T) {
	// The conversational path appends the run summary underneath the token. Taking
	// everything after the last colon used to swallow that whole block.
	response := "Human approval required for INR 3149.20. Approval token: tok-9\n\nAgent decision: ask_human\nReason: over the limit"
	markup := replyMarkupForResponse(response)
	if markup == nil || markup.InlineKeyboard[0][0].CallbackData != "/approve tok-9" {
		t.Fatalf("markup = %#v", markup)
	}
}

// A purchaser that can buy but cannot resolve an approval, which is the case the
// logging wrapper used to convert unchecked.
type buyOnlyPurchaser struct{}

func (buyOnlyPurchaser) Purchase(context.Context, buyer.PurchaseRequest) (buyer.PurchaseResult, error) {
	return buyer.PurchaseResult{}, nil
}

func (buyOnlyPurchaser) RequestApproval(context.Context, buyer.PurchaseRequest, string) (buyer.PurchaseResult, error) {
	return buyer.PurchaseResult{}, nil
}

func TestResolvingAnApprovalWithoutAResolverErrors(t *testing.T) {
	// The wrapper carries this method whatever it wraps, so the caller's check that
	// the purchaser can resolve approvals passes and the answer has to come from
	// here. An error keeps the bot up; the conversion this replaced did not.
	_, err := loggingPurchaser{inner: buyOnlyPurchaser{}}.ResolveApproval(t.Context(), 10, "tok-9", "approve")
	if err == nil {
		t.Fatal("a purchaser that cannot resolve approvals returned no error")
	}
}
