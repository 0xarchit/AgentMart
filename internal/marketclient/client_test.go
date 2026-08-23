// Tests for the merchant catalog client.
package marketclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/markettools"
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

func TestClientReadsMerchantCatalog(t *testing.T) {
	server := markettools.NewServer(fakeCatalog{})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client, err := New(t.Context(), httpServer.URL, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	products, err := client.Search(t.Context(), catalog.SearchRequest{Query: "trim"})
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].ID != "product" {
		t.Fatalf("products = %+v", products)
	}
	product, err := client.Get(t.Context(), "product")
	if err != nil || product.PricePaise != 100 {
		t.Fatalf("product = %+v, err = %v", product, err)
	}
	stock, err := client.CheckStock(t.Context(), "product", 1)
	if err != nil || !stock.Available || stock.Stock != 3 {
		t.Fatalf("stock = %+v, err = %v", stock, err)
	}
}
