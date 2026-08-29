// Tests for the buyer agent service: card discovery, bearer wall, quote-only
// contract, and input-required signalling for human approval.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/marketauth"
	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

type stubShopper struct {
	result  shopgraph.Result
	wallets []shopgraph.Wallet
}

func (s *stubShopper) Run(_ context.Context, _ string, wallet shopgraph.Wallet) (shopgraph.Result, error) {
	s.wallets = append(s.wallets, wallet)
	return s.result, nil
}

func TestBuyerAgentRequiresToken(t *testing.T) {
	if _, err := newBuyerAgentHandler(&stubShopper{}, "http://localhost:8082/a2a/", ""); err == nil {
		t.Fatal("expected the service to refuse to start without a token")
	}
}

func TestBuyerAgentServesCardBehindBearerWall(t *testing.T) {
	handler, err := newBuyerAgentHandler(&stubShopper{}, "http://localhost:8082/a2a/", "secret")
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous card status = %d, want 401", anonymous.Code)
	}

	authorized := httptest.NewRecorder()
	cardRequest := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	cardRequest.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(authorized, cardRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized card status = %d", authorized.Code)
	}
	var card struct {
		Name   string `json:"name"`
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(authorized.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != "agentmart-buyer" || len(card.Skills) != 1 || card.Skills[0].ID != "negotiate_purchase" {
		t.Fatalf("unexpected card: %+v", card)
	}
}

func TestBuyerAgentQuoteUsesStatedBudget(t *testing.T) {
	shopper := &stubShopper{result: shopgraph.Result{
		Action: shopgraph.ActionBuy, ProductID: "p1", ProductName: "TrimPro", Quantity: 1,
		FinalPaise: 240000, Accepted: true, Rationale: "within band",
		Transcript: []negotiation.Turn{{Actor: "merchant", Message: "offer"}},
	}}

	var handler http.Handler
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	var err error
	handler, err = newBuyerAgentHandler(shopper, httpServer.URL+"/a2a/", "secret")
	if err != nil {
		t.Fatal(err)
	}

	authed, err := marketauth.NewClient("secret", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	card, err := agentcard.NewResolver(authed).Resolve(t.Context(), httpServer.URL+"/a2a/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("resolve buyer card: %v", err)
	}
	client, err := a2aclient.NewFromCard(t.Context(), card, a2aclient.WithJSONRPCTransport(authed))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()

	ask, _ := json.Marshal(map[string]any{"request": "a trimmer", "budget_paise": 250000})
	if _, err := client.SendMessage(t.Context(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(string(ask))),
	}); err != nil {
		t.Fatalf("send shopping request: %v", err)
	}

	if len(shopper.wallets) != 1 {
		t.Fatalf("shopper runs = %d", len(shopper.wallets))
	}
	got := shopper.wallets[0]
	if got.BudgetPaise != 250000 || got.SpendLimitPaise != 250000 || got.BalancePaise != 250000 {
		t.Fatalf("stated budget not used as the ceiling: %+v", got)
	}
	if got.AccountID != "" {
		t.Fatalf("agent callers must stay account-less, got %q", got.AccountID)
	}
}
