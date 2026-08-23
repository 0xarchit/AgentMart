// Package markettools exposes the merchant catalog as typed read-only tools.
package markettools

import (
	"context"
	"fmt"

	"agentmart/internal/catalog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type catalogReader interface {
	Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error)
	Get(context.Context, string) (catalog.Product, error)
	CheckStock(context.Context, string, int) (catalog.StockResult, error)
}

// NewServer constructs the merchant catalog tool server.
func NewServer(reader catalogReader) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "merchant-catalog", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "search_catalog", Description: "Search the merchant catalog by text, category, or maximum price."}, searchHandler(reader))
	mcp.AddTool(server, &mcp.Tool{Name: "get_product", Description: "Read one authoritative merchant catalog product."}, getHandler(reader))
	mcp.AddTool(server, &mcp.Tool{Name: "check_stock", Description: "Check current stock for a product and quantity."}, stockHandler(reader))
	return server
}

type searchInput struct {
	Query         string `json:"query,omitempty"`
	Category      string `json:"category,omitempty"`
	MaxPricePaise int64  `json:"max_price_paise,omitempty"`
}

func searchHandler(reader catalogReader) mcp.ToolHandlerFor[searchInput, []catalog.Product] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, []catalog.Product, error) {
		products, err := reader.Search(ctx, catalog.SearchRequest{Query: input.Query, Category: input.Category, MaxPricePaise: input.MaxPricePaise})
		if err != nil {
			return nil, nil, fmt.Errorf("search catalog: %w", err)
		}
		return nil, products, nil
	}
}

type productInput struct {
	ProductID string `json:"product_id" jsonschema:"the catalog product identifier"`
}

func getHandler(reader catalogReader) mcp.ToolHandlerFor[productInput, catalog.Product] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input productInput) (*mcp.CallToolResult, catalog.Product, error) {
		product, err := reader.Get(ctx, input.ProductID)
		if err != nil {
			return nil, catalog.Product{}, fmt.Errorf("get product: %w", err)
		}
		return nil, product, nil
	}
}

type stockInput struct {
	ProductID string `json:"product_id" jsonschema:"the catalog product identifier"`
	Quantity  int    `json:"quantity" jsonschema:"the requested quantity"`
}

func stockHandler(reader catalogReader) mcp.ToolHandlerFor[stockInput, catalog.StockResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input stockInput) (*mcp.CallToolResult, catalog.StockResult, error) {
		stock, err := reader.CheckStock(ctx, input.ProductID, input.Quantity)
		if err != nil {
			return nil, catalog.StockResult{}, fmt.Errorf("check stock: %w", err)
		}
		return nil, stock, nil
	}
}
