// Package markettools exposes the merchant catalog as typed read-only tools.
package markettools

import (
	"context"
	"fmt"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type catalogReader interface {
	Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error)
	Get(context.Context, string) (catalog.Product, error)
	CheckStock(context.Context, string, int) (catalog.StockResult, error)
}

// offerReader supplies merchant-private pricing for the offers tool.
type offerReader interface {
	Get(context.Context, string) (catalog.Product, error)
	GetWithCost(context.Context, string) (catalog.Product, error)
}

// NewServer constructs the merchant catalog tool server.
func NewServer(reader catalogReader) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "merchant-catalog", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "search_catalog", Description: "Search the merchant catalog by text, category, or maximum price."}, searchHandler(reader))
	mcp.AddTool(server, &mcp.Tool{Name: "get_product", Description: "Read one authoritative merchant catalog product."}, getHandler(reader))
	mcp.AddTool(server, &mcp.Tool{Name: "check_stock", Description: "Check current stock for a product and quantity."}, stockHandler(reader))
	return server
}

// AddOffersTool exposes the agent-readable upsell surface: the merchant's
// opening quote for a product and quantity, including any combo bundle.
// Cost figures stay server-side; the response carries prices only.
func AddOffersTool(server *mcp.Server, reader offerReader) {
	mcp.AddTool(server, &mcp.Tool{Name: "get_offers", Description: "Read the merchant's current offer for a product and quantity, including combo bundle value."}, offersHandler(reader))
}

type offersInput struct {
	ProductID string `json:"product_id" jsonschema:"the catalog product identifier"`
	Quantity  int    `json:"quantity" jsonschema:"the requested quantity"`
}

type offersOutput struct {
	Kind       string                  `json:"kind"`
	BasePaise  int64                   `json:"base_amount_paise"`
	FinalPaise int64                   `json:"final_amount_paise"`
	Reason     string                  `json:"reason"`
	Bundle     *negotiation.BundleItem `json:"bundle,omitempty"`
}

func offersHandler(reader offerReader) mcp.ToolHandlerFor[offersInput, offersOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input offersInput) (*mcp.CallToolResult, offersOutput, error) {
		if input.Quantity <= 0 {
			return nil, offersOutput{}, fmt.Errorf("quantity must be positive")
		}
		mainPub, err := reader.Get(ctx, input.ProductID)
		if err != nil {
			return nil, offersOutput{}, fmt.Errorf("get product: %w", err)
		}
		mainPriv, err := reader.GetWithCost(ctx, input.ProductID)
		if err != nil {
			return nil, offersOutput{}, fmt.Errorf("price product: %w", err)
		}
		var partner *catalog.Product
		if mainPub.ComboWith != nil && mainPub.ComboDiscountPct > 0 {
			loaded, perr := reader.Get(ctx, *mainPub.ComboWith)
			if perr == nil {
				partner = &loaded
			}
		}
		var partnerPriced *negotiation.Priced
		if partner != nil {
			partnerPriv, perr := reader.GetWithCost(ctx, partner.ID)
			if perr == nil {
				partnerPriced = &negotiation.Priced{Product: *partner, CostPaise: partnerPriv.CostPaise}
			}
		}
		offer, oerr := negotiation.OpeningOffer(negotiation.Priced{Product: mainPriv, CostPaise: mainPriv.CostPaise}, partnerPriced, input.Quantity)
		if oerr != nil {
			return nil, offersOutput{}, fmt.Errorf("build offer: %w", oerr)
		}
		return nil, offersOutput{Kind: string(offer.Kind), BasePaise: offer.BasePaise, FinalPaise: offer.FinalPaise, Reason: offer.Reason, Bundle: offer.Bundle}, nil
	}
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
