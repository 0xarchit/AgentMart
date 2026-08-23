// Tests for the merchant catalog tool boundary.
package markettools

import (
	"context"
	"testing"

	"agentmart/internal/catalog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeCatalog struct{}

func (fakeCatalog) Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "product", Name: "Trimmer", PricePaise: 100, Stock: 3}}, nil
}

func (fakeCatalog) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "product", Name: "Trimmer", PricePaise: 100, Stock: 3}, nil
}

func (fakeCatalog) CheckStock(context.Context, string, int) (catalog.StockResult, error) {
	return catalog.StockResult{Available: true, Stock: 3}, nil
}

func TestCatalogToolsExposeTypedOperations(t *testing.T) {
	client, server := mcp.NewInMemoryTransports()
	merchant := NewServer(fakeCatalog{})
	go func() {
		_ = merchant.Run(t.Context(), server)
	}()

	buyer := mcp.NewClient(&mcp.Implementation{Name: "buyer", Version: "v1"}, nil)
	session, err := buyer.Connect(t.Context(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("tool count = %d", len(tools.Tools))
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_product", Arguments: map[string]any{"product_id": "product"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("result = %+v", result)
	}
}
