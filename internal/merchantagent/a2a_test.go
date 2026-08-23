// Tests for the merchant A2A negotiation boundary.
package merchantagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

func TestMerchantAgentCardIsDiscoverable(t *testing.T) {
	var handler http.Handler
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	var err error
	handler, err = NewHandler(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	}, negotiation.NewMemorySessionStore(), httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	card, err := agentcard.DefaultResolver.Resolve(t.Context(), httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "merchant-negotiation" || len(card.Skills) != 1 {
		t.Fatalf("card = %+v", card)
	}
	client, err := a2aclient.NewFromCard(t.Context(), card)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	message, _ := json.Marshal(map[string]any{"type": "propose", "product_id": "product", "qty": 1})
	result, err := client.SendMessage(t.Context(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(string(message)))})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected A2A result")
	}
}
