// Tests for market endpoint exposure and access control.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
)

type testCatalog struct{}

func (testCatalog) Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "product", PricePaise: 100}}, nil
}

func (testCatalog) Get(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "product", PricePaise: 100, Stock: 2}, nil
}

func (testCatalog) GetWithCost(context.Context, string) (catalog.Product, error) {
	return catalog.Product{ID: "product", PricePaise: 100, CostPaise: 70, Stock: 2}, nil
}

func (testCatalog) CheckStock(context.Context, string, int) (catalog.StockResult, error) {
	return catalog.StockResult{Available: true, Stock: 2}, nil
}

func TestProtectedMarketRoutes(t *testing.T) {
	handler, err := newHandler(testCatalog{}, negotiation.NewMemorySessionStore(), "http://merchant.test/a2a", "secret", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	public := httptest.NewRecorder()
	handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/health", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("health status = %d", public.Code)
	}

	for _, path := range []string{
		"/catalog/products/product",
		"/negotiation",
		"/mcp",
		"/a2a/",
	} {
		t.Run(path, func(t *testing.T) {
			private := httptest.NewRecorder()
			handler.ServeHTTP(private, httptest.NewRequest(http.MethodGet, path, nil))
			if private.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized status = %d", private.Code)
			}

			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer secret")
			authorized := httptest.NewRecorder()
			handler.ServeHTTP(authorized, request)
			if authorized.Code == http.StatusUnauthorized {
				t.Fatalf("authorized request was rejected")
			}
		})
	}
}
