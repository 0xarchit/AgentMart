// Tests for the merchant negotiation client.
package negotiationclient

import (
	"context"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
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
