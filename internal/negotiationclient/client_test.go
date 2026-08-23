// Tests for the merchant negotiation client.
package negotiationclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
)

func TestClientNegotiatesAgainstMerchantServer(t *testing.T) {
	server := negotiation.NewCatalogServer(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client, err := New(httpServer.URL+"/negotiation", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := client.Propose(t.Context(), "product", 1)
	if err != nil || proposal.SessionID == "" || proposal.BaseAmountPaise != 100 {
		t.Fatalf("proposal = %+v, err = %v", proposal, err)
	}
	resolution, err := client.Accept(t.Context(), proposal.SessionID)
	if err != nil || resolution.Status != "accepted" || resolution.ProductID != "product" || resolution.Quantity != 1 {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
}

func TestA2AClientNegotiatesAgainstMerchantServer(t *testing.T) {
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	var err error
	handler, err = merchantagent.NewHandler(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	}, negotiation.NewMemorySessionStore(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewA2A(t.Context(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	proposal, err := client.Propose(t.Context(), "product", 1)
	if err != nil || proposal.SessionID == "" || proposal.BaseAmountPaise != 100 {
		t.Fatalf("proposal = %+v, err = %v", proposal, err)
	}
	resolution, err := client.Accept(t.Context(), proposal.SessionID)
	if err != nil || resolution.Status != "accepted" || resolution.ProductID != "product" || resolution.Quantity != 1 {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
}
