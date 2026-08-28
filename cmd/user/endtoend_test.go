// End-to-end proof that a shopping request travels the whole system: buyer
// brief, shop conversation, quote, judgement, settlement. Both processes are
// wired as they are in production, against a stand-in model provider so the
// flow is provable without network access.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/campaigns"
	"agentmart/internal/catalog"
	"agentmart/internal/marketgraph"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	"agentmart/internal/shopgraph"
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
func marketProcess(t *testing.T, providerURL string) *httptest.Server {
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
		APIKey: "test-key", BaseURL: providerURL, Model: "stand-in",
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

	market := marketProcess(t, provider.URL)
	t.Cleanup(market.Close)

	merchantConversation, err := negotiationclient.NewAgentClient(t.Context(), market.URL, market.Client())
	if err != nil {
		t.Fatalf("reach the merchant: %v", err)
	}
	t.Cleanup(func() { _ = merchantConversation.Close() })

	buyer, err := shopgraph.New(t.Context(), shopgraph.Config{
		APIKey: "test-key", BaseURL: provider.URL, Model: "stand-in",
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
		shopgraph.Wallet{BalancePaise: 500000, SpendLimitPaise: 400000, AccountID: "account-1"})
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
