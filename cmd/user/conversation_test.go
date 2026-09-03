// Tests for per chat conversation memory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentmart/internal/buyer"
	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"
	"agentmart/internal/telegram"
)

// fakeRedis answers the two commands conversation memory uses.
func fakeRedis(t *testing.T, stored *string) *negotiation.RedisSessionStore {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "\"SET\"") {
			*stored = string(body)
			_, _ = w.Write([]byte(`{"result":"OK"}`))
			return
		}
		if *stored == "" {
			_, _ = w.Write([]byte(`{"result":null}`))
			return
		}
		var command []any
		_ = json.Unmarshal([]byte(*stored), &command)
		value, _ := json.Marshal(command[2])
		_, _ = w.Write([]byte(`{"result":` + string(value) + `}`))
	}))
	t.Cleanup(server.Close)
	store, err := negotiation.NewRedisSessionStore(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestConversationMemoryRemembersTheShortlist(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	saved := shopgraph.Conversation{
		Brief:   "a trimmer under 3000",
		Options: []shopgraph.PriorOption{{ProductID: "trim-nova", Name: "Nova", PricePaise: 179900}},
		Chosen:  "trim-nova",
	}
	if err := memory.Save(t.Context(), 42, saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Brief != saved.Brief || len(loaded.Options) != 1 || loaded.Options[0].ProductID != "trim-nova" {
		t.Fatalf("loaded = %+v", loaded)
	}
	// One person's memory, so a shortlist cannot surface in someone else's chat.
	if !strings.Contains(stored, "agentmart:chat:42") {
		t.Fatalf("memory is not scoped to the person: %s", stored)
	}
}

func TestUnreadableMemoryIsTreatedAsNoMemory(t *testing.T) {
	stored := `["SET","agentmart:chat:42","not json at all"]`
	memory := redisConversations{store: fakeRedis(t, &stored)}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatalf("unreadable memory should read as empty, not fail: %v", err)
	}
	if !loaded.Empty() {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestNothingRememberedYetIsNotAnError(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Empty() {
		t.Fatalf("a first message started with memory: %+v", loaded)
	}
}

func TestAnEmptyConversationIsWrittenSoAPurchaseIsForgotten(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	if err := memory.Save(t.Context(), 42, shopgraph.Conversation{}); err != nil {
		t.Fatal(err)
	}
	// Saving nothing has to reach the store. Skipping the write would leave the
	// bought shortlist in place for the next request to refine.
	if stored == "" {
		t.Fatal("forgetting a finished conversation wrote nothing")
	}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Empty() {
		t.Fatalf("a bought shortlist survived: %+v", loaded)
	}
}

// recordingMemory stands in for the store and keeps every write, so a test can
// assert both what a run was given and what it left behind.
type recordingMemory struct {
	prior shopgraph.Conversation
	loads int
	saved []shopgraph.Conversation
}

func (m *recordingMemory) Load(context.Context, int64) (shopgraph.Conversation, error) {
	m.loads++
	return m.prior, nil
}

func (m *recordingMemory) Save(_ context.Context, _ int64, prior shopgraph.Conversation) error {
	m.saved = append(m.saved, prior)
	return nil
}

func (m *recordingMemory) last() shopgraph.Conversation {
	if len(m.saved) == 0 {
		return shopgraph.Conversation{}
	}
	return m.saved[len(m.saved)-1]
}

// fundedAccounts can afford what this shelf quotes, so a run reaches a purchase
// instead of stopping at the spend limit.
type fundedAccounts struct{}

func (fundedAccounts) AccountForTelegram(context.Context, int64) (buyer.Account, error) {
	return buyer.Account{ID: "account-1", WalletBalancePaise: 700000, SpendLimitPaise: 600000}, nil
}

// failingAuditor refuses to write the reasoning trail, which ends the request.
type failingAuditor struct{}

func (failingAuditor) RecordAgentRun(context.Context, int64, string, buyer.AgentRun) error {
	return errors.New("the reasoning trail is unwritable")
}

// recordingAuditor keeps every reasoning trail it is handed, so a test can assert
// what a purchase was justified with rather than only that a write happened.
type recordingAuditor struct {
	ids      []int64
	requests []string
	runs     []buyer.AgentRun
	// delay makes this write take measurable time. It is the seam that lets a
	// test tell the moment the shop quoted apart from the moment the purchase was
	// assembled, because everything between the two is otherwise instant.
	delay time.Duration
}

func (a *recordingAuditor) RecordAgentRun(_ context.Context, telegramID int64, request string, run buyer.AgentRun) error {
	a.ids = append(a.ids, telegramID)
	a.requests = append(a.requests, request)
	a.runs = append(a.runs, run)
	time.Sleep(a.delay)
	return nil
}

// capturingPurchaser keeps the request the conversation assembled, so what the
// gate is told can be compared with what the shop actually said.
type capturingPurchaser struct {
	fakePurchaser
	seen []buyer.PurchaseRequest
}

func (c *capturingPurchaser) Purchase(_ context.Context, request buyer.PurchaseRequest) (buyer.PurchaseResult, error) {
	c.seen = append(c.seen, request)
	return c.result, c.err
}

// The store is wired, but nothing proved the request path used it. This drives a
// real graph against a remembered shortlist and then buys, which is both halves
// of the contract: a follow up is answered in context, and a settled purchase
// stops being context.
func TestAFollowUpIsShoppedAgainstMemoryAndABuyClearsIt(t *testing.T) {
	buyerService, _ := shoppingSystem(t)
	client, record := recordingBot(t)
	memory := &recordingMemory{prior: shopgraph.Conversation{
		Brief:   "buy me a good trimmer",
		Options: []shopgraph.PriorOption{{ProductID: "trim-3", Name: "BladeMaster Lite", PricePaise: 129900}},
	}}

	err := conversationalBuy(t.Context(), client,
		fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 388013, OrderID: "order-1"}},
		commandServices{
			loop: buyerService, accounts: fundedAccounts{}, catalog: stockCatalog{},
			negotiations: fakeNegotiator{}, conversations: memory,
		},
		&telegram.Message{MessageID: 8, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "the other one"})
	if err != nil {
		t.Fatal(err)
	}
	if memory.loads != 1 {
		t.Fatalf("memory was read %d times, want once", memory.loads)
	}
	// The shop is asked the follow up together with what was already on screen, so
	// "the other one" refers to something.
	asked := strings.Join(record.messages, " ")
	for _, want := range []string{"the other one", "BladeMaster Lite"} {
		if !strings.Contains(asked, want) {
			t.Fatalf("the shop was not asked with %q: %v", want, record.messages)
		}
	}
	if len(memory.saved) != 2 {
		t.Fatalf("wrote memory %d times, want the run kept then the purchase forgotten: %+v", len(memory.saved), memory.saved)
	}
	// A refinement narrows the original ask rather than replacing it.
	if memory.saved[0].Brief != "buy me a good trimmer" {
		t.Fatalf("the first ask was overwritten: %+v", memory.saved[0])
	}
	// A bought shortlist is not one to refine, so the next message starts fresh.
	if last := memory.last(); !last.Empty() || last.Chosen != "" {
		t.Fatalf("a settled purchase left the chat remembering %+v", last)
	}
}

// A run that broke after the shop answered is exactly when a person says "try
// again" or "the second one". Recording the shortlist after the failure check
// threw that away, so this pins the order: what the shop showed is kept first.
func TestAFailedRunKeepsWhatTheShopShowed(t *testing.T) {
	buyerService, _ := shoppingSystem(t, "choose")
	client, record := recordingBot(t)
	memory := &recordingMemory{}

	err := conversationalBuy(t.Context(), client, fakePurchaser{},
		commandServices{
			loop: buyerService, accounts: escalatingAccounts{}, catalog: stockCatalog{},
			negotiations: fakeNegotiator{}, conversations: memory,
		},
		&telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "buy me a good trimmer"})
	// The failure is reported to the person, not returned as a broken request.
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.saved) != 1 {
		t.Fatalf("wrote memory %d times, want once: %+v", len(memory.saved), memory.saved)
	}
	kept := memory.saved[0]
	if kept.Brief != "buy me a good trimmer" || len(kept.Options) == 0 {
		t.Fatalf("a failed run lost its shortlist: %+v", kept)
	}
	var ids []string
	for _, option := range kept.Options {
		ids = append(ids, option.ProductID)
	}
	if !strings.Contains(strings.Join(ids, " "), "trim-9") {
		t.Fatalf("the shortlist kept was not the one shown: %v", ids)
	}
	// Strict mode: nothing is dressed up as a purchase.
	if sent := strings.Join(record.messages, " "); strings.Contains(sent, "fulfilled") {
		t.Fatalf("a failed run read as a purchase: %v", record.messages)
	}
}

// The audit write is the one thing that fails the whole request rather than being
// reported, because a purchase nobody can justify is worse than no purchase. The
// shortlist still has to survive it: the run itself was fine.
func TestAnAuditFailureEndsTheRequestButKeepsTheShortlist(t *testing.T) {
	buyerService, _ := shoppingSystem(t)
	client, _ := recordingBot(t)
	memory := &recordingMemory{}

	err := conversationalBuy(t.Context(), client, fakePurchaser{},
		commandServices{
			loop: buyerService, accounts: escalatingAccounts{}, catalog: stockCatalog{},
			negotiations: fakeNegotiator{}, conversations: memory, audit: failingAuditor{},
		},
		&telegram.Message{MessageID: 10, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "buy me a good trimmer"})
	if err == nil {
		t.Fatal("an unwritable reasoning trail must not read as a completed request")
	}
	if !strings.Contains(err.Error(), "audit agent run") {
		t.Fatalf("error lost what failed: %v", err)
	}
	if len(memory.saved) != 1 || len(memory.saved[0].Options) == 0 {
		t.Fatalf("the shortlist did not survive the audit failure: %+v", memory.saved)
	}
}

// An approval settles the purchase the shortlist was for; a rejection settles
// nothing. Both answers arrive on the command path rather than the shopping one,
// so neither is covered by the run tests above.
func TestAnApprovalClearsTheConversationAndARejectionKeepsIt(t *testing.T) {
	settled := &recordingMemory{}
	_, err := responseForCommandWithServices(t.Context(), fakeLinker{},
		fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 45000, OrderID: "order-1"}},
		fakeRefunder{}, 10, 5, []string{"/approve", "token"}, commandServices{conversations: settled})
	if err != nil {
		t.Fatal(err)
	}
	if len(settled.saved) != 1 || !settled.last().Empty() || settled.last().Chosen != "" {
		t.Fatalf("an approved purchase left the chat remembering %+v", settled.saved)
	}

	// A person who declined one ask may still want to refine it, so the shortlist
	// stays and the next message can narrow it.
	declined := &recordingMemory{}
	_, err = responseForCommandWithServices(t.Context(), fakeLinker{},
		fakePurchaser{result: buyer.PurchaseResult{Reason: "declined by the person"}},
		fakeRefunder{}, 10, 5, []string{"/reject", "token"}, commandServices{conversations: declined})
	if err != nil {
		t.Fatal(err)
	}
	if len(declined.saved) != 0 {
		t.Fatalf("a rejection cleared the conversation: %+v", declined.saved)
	}
}

// TestAnAgentRunIsRecordedBeforeTheMoneyMoves covers what the audit failure test
// cannot: that the trail actually explains the purchase. A hollowed-out payload
// still writes a row and still passes every other test, and a row that does not
// say what was bought, for whom, or why is not an audit trail.
func TestAnAgentRunIsRecordedBeforeTheMoneyMoves(t *testing.T) {
	buyerService, _ := shoppingSystem(t)
	client, _ := recordingBot(t)
	recorded := &recordingAuditor{}

	err := conversationalBuy(t.Context(), client,
		fakePurchaser{result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 388013, OrderID: "order-1"}},
		commandServices{
			loop: buyerService, accounts: fundedAccounts{}, catalog: stockCatalog{},
			negotiations: fakeNegotiator{}, audit: recorded,
		},
		&telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 77}, Text: "buy me a good trimmer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded.runs) != 1 {
		t.Fatalf("recorded %d agent runs, want exactly one", len(recorded.runs))
	}
	if recorded.ids[0] != 77 {
		t.Fatalf("the trail was credited to %d, want the person who asked", recorded.ids[0])
	}
	if recorded.requests[0] != "buy me a good trimmer" {
		t.Fatalf("the trail records the request as %q, want the person's own words", recorded.requests[0])
	}
	run := recorded.runs[0]
	if run.Action == "" || run.ProductID == "" || run.FinalPaise <= 0 {
		t.Fatalf("the trail does not say what was bought: %+v", run)
	}
	if run.Rationale == "" {
		t.Fatalf("the trail does not say why: %+v", run)
	}
}

// TestTheGateIsToldWhenTheShopQuoted pins the one wire that gives the gate's
// stale price rail something to measure. The rail itself is covered, and it
// refuses a zero observation time, so deleting the plumbing would be loud.
// Handing the gate the clock instead would be silent: every purchase would look
// freshly quoted forever and every other test would still pass, because they all
// build the gate request themselves. This one drives the real graph and reads
// what the conversation actually sent.
func TestTheGateIsToldWhenTheShopQuoted(t *testing.T) {
	buyerService, _ := shoppingSystem(t)
	client, _ := recordingBot(t)
	purchases := &capturingPurchaser{fakePurchaser: fakePurchaser{
		result: buyer.PurchaseResult{Fulfilled: true, AmountPaise: 388013, OrderID: "order-1"},
	}}
	// Long enough to separate the two moments well past any clock granularity,
	// short enough that the suite does not notice.
	const held = 60 * time.Millisecond

	err := conversationalBuy(t.Context(), client, purchases,
		commandServices{
			loop: buyerService, accounts: fundedAccounts{}, catalog: stockCatalog{},
			negotiations: fakeNegotiator{}, audit: &recordingAuditor{delay: held},
		},
		&telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 77}, Text: "buy me a good trimmer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(purchases.seen) != 1 {
		t.Fatalf("sent %d purchases, want exactly one", len(purchases.seen))
	}
	quoted := purchases.seen[0].PriceObservedAt
	if quoted.IsZero() {
		t.Fatal("the gate was told nothing about when the price was quoted, so every purchase is stale")
	}
	if age := time.Since(quoted); age < held {
		t.Fatalf("the observed price is %s old, want at least %s. The gate is being handed the clock at purchase time rather than when the shop quoted, which makes a stale price impossible to detect.", age, held)
	}
}
