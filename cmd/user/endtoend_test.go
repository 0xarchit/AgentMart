// End-to-end proof that a shopping request travels the whole system: buyer
// brief, shop conversation, quote, judgement, settlement. Both processes are
// wired as they are in production, against a stand-in model provider so the
// flow is provable without network access.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentmart/internal/buyer"
	"agentmart/internal/campaigns"
	"agentmart/internal/catalog"
	"agentmart/internal/marketgraph"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	"agentmart/internal/shopgraph"
	"agentmart/internal/telegram"
)

// demoStock is what the shop holds during the test.
func demoStock() []catalog.Product {
	partner := "oil-1"
	return []catalog.Product{
		{ID: "trim-9", Name: "BladeMaster Pro 9", Category: "trimmer", PricePaise: 349900, CostPaise: 210000, Stock: 6, WarrantyYears: 3, TrustScore: 92, ComboWith: &partner, ComboDiscountPct: 15},
		{ID: "trim-3", Name: "BladeMaster Lite", Category: "trimmer", PricePaise: 129900, CostPaise: 90000, Stock: 40, WarrantyYears: 1, TrustScore: 71},
		{ID: "oil-1", Name: "Beard Oil", Category: "beard_oil", PricePaise: 39900, CostPaise: 18000, Stock: 30, WarrantyYears: 0, TrustScore: 80},
	}
}

// standInProvider answers the reasoning calls of both processes. It reads the
// instruction it was sent and returns the shape that stage expects, which also
// asserts that every stage really does send its instruction and ask for a
// structured answer.
func standInProvider(t *testing.T, seen map[string]int, refuse ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("provider got undecodable request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var instruction, facts string
		for _, message := range request.Messages {
			if message.Role == "system" {
				instruction += message.Content
			}
			if message.Role == "user" {
				facts += message.Content
			}
		}
		if strings.TrimSpace(instruction) == "" {
			t.Error("a stage called the provider with no instruction")
		}
		stage, answer := stageAnswer(instruction, facts)
		if stage == "" {
			t.Errorf("unrecognised stage, instruction began: %.80s", instruction)
			http.Error(w, "unknown stage", http.StatusInternalServerError)
			return
		}
		seen[stage]++
		// The shop must hear the person's own words, not an envelope of facts
		// meant for other stages.
		if stage == "shopfront" {
			if !strings.Contains(facts, "buy me a good trimmer") {
				t.Errorf("the shop was not told what was asked for: %s", facts)
			}
			if strings.Contains(facts, "wallet_balance_paise") {
				t.Errorf("the shop was handed the buyer's wallet facts: %s", facts)
			}
		}
		for _, refused := range refuse {
			if refused == stage {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"message": "rate limit reached for this key"},
				})
				return
			}
		}

		function := "final_answer"
		if len(request.Tools) > 0 {
			function = request.Tools[0].Function.Name
		}
		encoded, err := json.Marshal(answer)
		if err != nil {
			t.Fatalf("encode stage answer: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id": "call-1", "type": "function",
						"function": map[string]any{"name": function, "arguments": string(encoded)},
					}},
				},
			}},
		})
	}))
}

// stageAnswer maps an instruction to the stage that owns it and the answer that
// stage must return.
func stageAnswer(instruction, facts string) (string, any) {
	switch {
	case strings.Contains(instruction, "You own this shop"):
		// The shop pitches two real products and one it does not stock, to prove
		// the invented one is dropped before anyone sees a price.
		return "shopfront", map[string]any{
			"greeting": "welcome in, good day for a trimmer",
			"options": []any{
				map[string]any{"product_id": "trim-9", "pitch": "our best build", "includes": "beard oil at 15 percent off"},
				map[string]any{"product_id": "trim-3", "pitch": "fine for daily upkeep"},
				map[string]any{"product_id": "not-in-stock", "pitch": "invented"},
			},
			"closing": "say the word and I will price one up",
		}
	case strings.Contains(instruction, "The shop has shown you what it has"):
		return "choose", map[string]any{"product_id": "trim-9", "quantity": 1, "rationale": "three year warranty justifies the price"}
	case strings.Contains(instruction, "pricing strategist"):
		return "strategist", map[string]any{"strategy": "hold", "amount_paise": 349900, "reason": "stock is healthy and the ask is fair"}
	case strings.Contains(instruction, "You are AgentMart's buyer"):
		if strings.Contains(facts, "premium_over_list_paise") || strings.Contains(facts, "session_id") {
			return "assess", map[string]any{"decision": "accept", "reason": "within budget and the warranty carries the price"}
		}
		return "assess", map[string]any{"decision": "accept", "reason": "within budget"}
	}
	return "", nil
}

// marketProcess wires the merchant exactly as its binary does: one server with
// the strategist, the shop-owner voice, and the cost floor, exposed on the
// conversation surface.
// reasoningWiring is where both processes get their model from. The stand-in
// server is used by default; a live run points it at the real endpoint.
type reasoningWiring struct {
	baseURL string
	apiKey  string
	model   string
}

func marketProcess(t *testing.T, wiring reasoningWiring) *httptest.Server {
	t.Helper()
	getProduct := func(_ context.Context, id string) (catalog.Product, error) {
		for _, product := range demoStock() {
			if product.ID == id {
				return product, nil
			}
		}
		return catalog.Product{}, fmt.Errorf("product %s not found", id)
	}
	search := func(_ context.Context, _ catalog.SearchRequest) ([]catalog.Product, error) {
		return demoStock(), nil
	}
	getPriced := func(ctx context.Context, id string) (catalog.Product, int64, error) {
		product, err := getProduct(ctx, id)
		if err != nil {
			return catalog.Product{}, 0, err
		}
		return product, product.CostPaise, nil
	}

	merchant, err := marketgraph.New(marketgraph.Config{
		APIKey: wiring.apiKey, BaseURL: wiring.baseURL, Model: wiring.model,
	}, campaigns.NewProvider(nil), nil)
	if err != nil {
		t.Fatalf("merchant reasoning: %v", err)
	}
	if merchant == nil {
		t.Fatal("merchant reasoning was not built")
	}

	server, err := negotiation.NewOrchestratedServer(getProduct, getPriced, negotiation.NewMemorySessionStore())
	if err != nil {
		t.Fatalf("negotiation server: %v", err)
	}
	server.UseNegotiator(merchant).WithShopfront(merchant, search)

	var handler http.Handler
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	agentHandler, err := merchantagent.NewHandler(server, httpServer.URL)
	if err != nil {
		t.Fatalf("merchant agent surface: %v", err)
	}
	handler = agentHandler
	return httpServer
}

// shoppingSystem wires both processes and returns a buyer ready to run.
func shoppingSystem(t *testing.T, refuse ...string) (*shopgraph.Service, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	provider := standInProvider(t, seen, refuse...)
	t.Cleanup(provider.Close)
	wiring := reasoningWiring{baseURL: provider.URL, apiKey: "test-key", model: "stand-in"}

	market := marketProcess(t, wiring)
	t.Cleanup(market.Close)

	merchantConversation, err := negotiationclient.NewAgentClient(t.Context(), market.URL, market.Client())
	if err != nil {
		t.Fatalf("reach the merchant: %v", err)
	}
	t.Cleanup(func() { _ = merchantConversation.Close() })

	buyer, err := shopgraph.New(t.Context(), shopgraph.Config{
		APIKey: wiring.apiKey, BaseURL: wiring.baseURL, Model: wiring.model,
	}, shopgraph.Tools{
		Browse:  merchantConversation.Browse,
		Get:     func(_ context.Context, id string) (catalog.Product, error) { return catalog.Product{ID: id}, nil },
		Offers:  merchantConversation.ProposeAs,
		Counter: merchantConversation.Counter,
		Accept:  merchantConversation.Accept,
		Decline: merchantConversation.Decline,
	})
	if err != nil {
		t.Fatalf("build the buyer: %v", err)
	}
	return buyer, seen
}

func TestAShoppingRequestTravelsTheWholeSystem(t *testing.T) {
	buyer, seen := shoppingSystem(t)

	var stages []string
	result, err := buyer.RunWithProgress(t.Context(), "buy me a good trimmer",
		shopgraph.Wallet{BalancePaise: 700000, SpendLimitPaise: 600000, AccountID: "account-1"},
		func(line string) { stages = append(stages, line) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if result.Action != shopgraph.ActionBuy {
		t.Fatalf("action = %q, want a purchase. rationale=%q stages seen=%v progress=%v", result.Action, result.Rationale, seen, stages)
	}
	if result.ProductID != "trim-9" || result.ProductName != "BladeMaster Pro 9" {
		t.Fatalf("bought %q named %q, want the chosen product with its catalog name", result.ProductID, result.ProductName)
	}
	// The merchant is allowed to price above list for what it bundles in, so
	// long as it stays inside the person's money.
	if result.FinalPaise < 349900 || result.FinalPaise > 600000 {
		t.Fatalf("final = %d, want at least list and within the spend limit", result.FinalPaise)
	}
	if result.SessionID == "" {
		t.Fatal("a settled purchase needs a session to audit")
	}

	// Both sides of the conversation are recorded, starting with the browse.
	if len(result.Transcript) < 3 {
		t.Fatalf("transcript has %d turns, want the browse turns plus the quote", len(result.Transcript))
	}
	opening := result.Transcript[0]
	if opening.Actor != "buyer" || !strings.Contains(opening.Message, "trimmer") {
		t.Fatalf("first turn = %+v, want the buyer's brief", opening)
	}
	shopTurn := result.Transcript[1]
	if shopTurn.Actor != "merchant" || !strings.Contains(shopTurn.Message, "BladeMaster Pro 9") {
		t.Fatalf("second turn = %+v, want the shop's pitch", shopTurn)
	}
	if strings.Contains(shopTurn.Message, "not-in-stock") {
		t.Fatal("the shop pitched a product it does not hold")
	}
	if !strings.Contains(shopTurn.Message, "3499.00") {
		t.Fatalf("the pitch must quote the catalog price: %s", shopTurn.Message)
	}

	// Every reasoning stage on both sides was actually consulted.
	for _, stage := range []string{"shopfront", "choose", "assess"} {
		if seen[stage] == 0 {
			t.Fatalf("stage %q never ran, stages seen: %v", stage, seen)
		}
	}
	if len(stages) < 3 {
		t.Fatalf("progress reported %d stages, want the conversation narrated: %v", len(stages), stages)
	}
}

func TestAnOfferOverTheLimitGoesToThePerson(t *testing.T) {
	buyer, _ := shoppingSystem(t)

	result, err := buyer.Run(t.Context(), "buy me a good trimmer",
		shopgraph.Wallet{BalancePaise: 500000, SpendLimitPaise: 350000, AccountID: "account-1"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The buyer agent said accept. The money guard may only downgrade that to a
	// question for the person, never silently spend over the stated limit.
	if result.Action != shopgraph.ActionAskHuman {
		t.Fatalf("action = %q, want the person to be asked", result.Action)
	}
	if !result.NeedsApproval {
		t.Fatal("an escalated offer must be marked as needing approval")
	}
	if !strings.Contains(result.Rationale, "above the stated limit") {
		t.Fatalf("rationale = %q, want it to say why the person was asked", result.Rationale)
	}
	if len(result.Transcript) < 3 {
		t.Fatalf("transcript has %d turns, want the conversation that led here", len(result.Transcript))
	}
}

func TestALostJudgementGoesToThePersonInsteadOfLosingTheRun(t *testing.T) {
	// The provider refuses the last call of the run, after the shop has already
	// pitched, the buyer has chosen, and a price is on the table.
	buyer, seen := shoppingSystem(t, "assess")

	var stages []string
	result, err := buyer.RunWithProgress(t.Context(), "buy me a good trimmer",
		shopgraph.Wallet{BalancePaise: 700000, SpendLimitPaise: 600000, AccountID: "account-1"},
		func(line string) { stages = append(stages, line) })
	if err != nil {
		t.Fatalf("a lost judgement must not lose the run: %v", err)
	}

	if result.Action != shopgraph.ActionAskHuman || !result.NeedsApproval {
		t.Fatalf("action = %q approval = %v, want the person asked", result.Action, result.NeedsApproval)
	}
	if !strings.Contains(result.Rationale, "could not judge") {
		t.Fatalf("rationale = %q, want it to say the judgement was lost", result.Rationale)
	}
	// The person is told which layer failed, not just that something did.
	if !strings.Contains(strings.Join(stages, " "), "reasoning layer") {
		t.Fatalf("stages must name the failing layer: %v", stages)
	}
	if result.FinalPaise == 0 || len(result.Transcript) < 3 {
		t.Fatalf("the person needs the quote and the conversation: %d paise, %d turns", result.FinalPaise, len(result.Transcript))
	}
	if seen["assess"] == 0 {
		t.Fatal("the judgement was never attempted")
	}
}

// liveWiring reads the real endpoint from .env so a run can be proved against
// the provider the binaries actually use.
func liveWiring(t *testing.T) reasoningWiring {
	t.Helper()
	if os.Getenv("LIVE_PROVIDER") != "1" {
		t.Skip("set LIVE_PROVIDER=1 to spend real quota")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Skipf("no .env to read: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, found := strings.Cut(line, "="); found {
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	key := values["OPENAI_API_KEY_USER"]
	if key == "" {
		key = values["OPENAI_API_KEY"]
	}
	wiring := reasoningWiring{baseURL: values["OPENAI_BASE_URL"], apiKey: key, model: values["ADK_MODEL_NAME"]}
	if wiring.baseURL == "" || wiring.apiKey == "" || wiring.model == "" {
		t.Fatal("OPENAI_BASE_URL, an API key, and ADK_MODEL_NAME must all be set")
	}
	t.Logf("endpoint %s model %s", wiring.baseURL, wiring.model)
	return wiring
}

func TestLiveShoppingRunAgainstTheRealProvider(t *testing.T) {
	wiring := liveWiring(t)

	market := marketProcess(t, wiring)
	t.Cleanup(market.Close)
	merchantConversation, err := negotiationclient.NewAgentClient(t.Context(), market.URL, market.Client())
	if err != nil {
		t.Fatalf("reach the merchant: %v", err)
	}
	t.Cleanup(func() { _ = merchantConversation.Close() })

	buyer, err := shopgraph.New(t.Context(), shopgraph.Config{
		APIKey: wiring.apiKey, BaseURL: wiring.baseURL, Model: wiring.model,
	}, shopgraph.Tools{
		Browse:  merchantConversation.Browse,
		Get:     func(_ context.Context, id string) (catalog.Product, error) { return catalog.Product{ID: id}, nil },
		Offers:  merchantConversation.ProposeAs,
		Counter: merchantConversation.Counter,
		Accept:  merchantConversation.Accept,
		Decline: merchantConversation.Decline,
	})
	if err != nil {
		t.Fatalf("build the buyer: %v", err)
	}

	started := time.Now()
	result, err := buyer.RunWithProgress(t.Context(), "i want a trimmer",
		shopgraph.Wallet{BalancePaise: 1045000, SpendLimitPaise: 250000, AccountID: "account-1"},
		func(line string) { t.Logf("[%s] %s", time.Since(started).Round(time.Second), line) })
	if err != nil {
		t.Fatalf("live run failed after %s: %v", time.Since(started).Round(time.Second), err)
	}
	t.Logf("finished in %s: %s %s at INR %.2f, action %s",
		time.Since(started).Round(time.Second), result.ProductID, result.ProductName,
		float64(result.FinalPaise)/100, result.Action)
	for _, turn := range result.Transcript {
		t.Logf("  %s: %s", turn.Actor, turn.Message)
	}

	if result.Action == "" {
		t.Fatal("a run must end in an action")
	}
	if result.ProductID == "" || result.FinalPaise == 0 {
		t.Fatalf("a run must name a product and a price, got %q at %d", result.ProductID, result.FinalPaise)
	}
	if len(result.Transcript) < 3 {
		t.Fatalf("transcript has %d turns, want the browse turns plus the quote", len(result.Transcript))
	}
}

// telegramRecorder stands in for Telegram and counts what the bot sends.
type telegramRecorder struct {
	documents int
	markups   []string
	messages  []string
}

// recordingBot returns a client wired to a recorder.
func recordingBot(t *testing.T) (*telegram.Client, *telegramRecorder) {
	t.Helper()
	record := &telegramRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendDocument"):
			record.documents++
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			record.messages = append(record.messages, string(body))
			if strings.Contains(string(body), "reply_markup") {
				record.markups = append(record.markups, string(body))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegram.NewClient("token", &http.Client{
		Transport: rewriteTelegramTransport{base: server.URL, next: server.Client().Transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, record
}

// escalatingAccounts has a spend limit below what the merchant will quote. The
// limit is stated against the quote this shelf actually produces, so the test
// exercises the guard rather than the size of an uplift.
type escalatingAccounts struct{}

func (escalatingAccounts) AccountForTelegram(context.Context, int64) (buyer.Account, error) {
	return buyer.Account{ID: "account-1", WalletBalancePaise: 500000, SpendLimitPaise: 350000}, nil
}

type stockCatalog struct{}

func (stockCatalog) Get(_ context.Context, id string) (catalog.Product, error) {
	for _, product := range demoStock() {
		if product.ID == id {
			return product, nil
		}
	}
	return catalog.Product{}, fmt.Errorf("not found")
}

func TestAnEscalationSendsOneTranscriptAndTwoButtons(t *testing.T) {
	buyerService, _ := shoppingSystem(t)
	client, record := recordingBot(t)

	err := conversationalBuy(t.Context(), client, fakePurchaser{result: buyer.PurchaseResult{AmountPaise: 388013}},
		commandServices{
			loop:         buyerService,
			accounts:     escalatingAccounts{},
			catalog:      stockCatalog{},
			negotiations: fakeNegotiator{},
		},
		&telegram.Message{MessageID: 7, Chat: telegram.Chat{ID: 10}, From: telegram.User{ID: 10}, Text: "buy me a good trimmer"})
	if err != nil {
		t.Fatalf("escalation must not fail: %v", err)
	}

	// The conversation is evidence, and evidence is sent once.
	if record.documents != 1 {
		t.Fatalf("sent %d transcripts, want exactly one", record.documents)
	}
	// A person handed a token with no way to answer it is a dead end.
	if len(record.markups) == 0 {
		t.Fatalf("the approval prompt carried no buttons, messages: %v", record.messages)
	}
	last := record.markups[len(record.markups)-1]
	if !strings.Contains(last, `/approve token`) || !strings.Contains(last, `/reject token`) {
		t.Fatalf("buttons must approve and decline the pending token: %s", last)
	}
}

func TestApprovalMarkupNeedsAToken(t *testing.T) {
	if approvalMarkup("  ") != nil {
		t.Fatal("no token means no buttons, not buttons that resolve nothing")
	}
	markup := approvalMarkup("abc123")
	if markup == nil || markup.InlineKeyboard[0][0].CallbackData != "/approve abc123" {
		t.Fatalf("markup = %#v", markup)
	}
}
