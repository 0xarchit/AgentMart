// Package marketclient reads merchant catalog facts through the service boundary.
package marketclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentmart/internal/catalog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const reconnectDialTimeout = 5 * time.Second

// Client calls the merchant's read-only catalog tools. The MCP session is
// long-lived but transparently re-dialed when the market binary restarts or
// the stream idles out.
type Client struct {
	mu       sync.Mutex
	endpoint string
	http     *http.Client
	session  *mcp.ClientSession
}

// New connects to a streamable merchant catalog endpoint.
func New(ctx context.Context, endpoint string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("merchant catalog endpoint is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "buyer-catalog", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect merchant catalog: %w", err)
	}
	return &Client{mu: sync.Mutex{}, endpoint: endpoint, http: httpClient, session: session}, nil
}

// Close releases the merchant catalog session. Shutdown-order races (market
// already gone) are treated as success — nothing to release.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	if err := c.session.Close(); err != nil {
		var msg string
		if strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "closing") {
			_ = msg
			return nil
		}
		return err
	}
	return nil
}

// Search returns merchant products matching the supplied filters.
func (c *Client) Search(ctx context.Context, request catalog.SearchRequest) ([]catalog.Product, error) {
	result, err := c.call(ctx, "search_catalog", map[string]any{"query": request.Query, "category": request.Category, "max_price_paise": request.MaxPricePaise})
	if err != nil {
		return nil, err
	}
	var products []catalog.Product
	if err := decodeStructured(result, &products); err != nil {
		return nil, fmt.Errorf("decode merchant catalog search: %w", err)
	}
	return products, nil
}

// Get returns one authoritative merchant product.
func (c *Client) Get(ctx context.Context, productID string) (catalog.Product, error) {
	if strings.TrimSpace(productID) == "" {
		return catalog.Product{}, fmt.Errorf("product id is required")
	}
	result, err := c.call(ctx, "get_product", map[string]any{"product_id": productID})
	if err != nil {
		return catalog.Product{}, err
	}
	var product catalog.Product
	if err := decodeStructured(result, &product); err != nil {
		return catalog.Product{}, fmt.Errorf("decode merchant product: %w", err)
	}
	return product, nil
}

// CheckStock returns current merchant stock facts.
func (c *Client) CheckStock(ctx context.Context, productID string, quantity int) (catalog.StockResult, error) {
	if strings.TrimSpace(productID) == "" || quantity <= 0 {
		return catalog.StockResult{}, fmt.Errorf("product id and positive quantity are required")
	}
	result, err := c.call(ctx, "check_stock", map[string]any{"product_id": productID, "quantity": quantity})
	if err != nil {
		return catalog.StockResult{}, err
	}
	var stock catalog.StockResult
	if err := decodeStructured(result, &stock); err != nil {
		return catalog.StockResult{}, fmt.Errorf("decode merchant stock: %w", err)
	}
	return stock, nil
}

func (c *Client) call(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if c == nil || c.session == nil {
		return nil, fmt.Errorf("merchant catalog client is not connected")
	}
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		// Long-lived MCP sessions die when the market binary restarts or the
		// stream idles out. Reconnect once and retry before giving up.
		if reconnectErr := c.reconnect(ctx); reconnectErr != nil {
			return nil, fmt.Errorf("call merchant catalog tool %s: %w (reconnect failed: %v)", name, err, reconnectErr)
		}
		result, retryErr := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if retryErr != nil {
			return nil, fmt.Errorf("call merchant catalog tool %s after reconnect: %w", name, retryErr)
		}
		if result.IsError {
			return nil, fmt.Errorf("merchant catalog tool %s failed: %s", name, toolError(result))
		}
		return result, nil
	}
	if result.IsError {
		return nil, fmt.Errorf("merchant catalog tool %s failed: %s", name, toolError(result))
	}
	return result, nil
}

// reconnect drops the dead session and dials a fresh one on its own short
// timeout so it works even when the caller's context is already expiring.
func (c *Client) reconnect(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(context.Background(), reconnectDialTimeout)
	defer cancel()
	ctx = dialCtx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		_ = c.session.Close()
	}
	transport := &mcp.StreamableClientTransport{Endpoint: c.endpoint, HTTPClient: c.http}
	client := mcp.NewClient(&mcp.Implementation{Name: "buyer-catalog", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	c.session = session
	return nil
}

func decodeStructured(result *mcp.CallToolResult, destination any) error {
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, destination)
	}
	if len(result.Content) == 0 {
		return fmt.Errorf("merchant tool returned no content")
	}
	encoded, err := json.Marshal(result.Content[0])
	if err != nil {
		return err
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(encoded, &content); err != nil {
		return err
	}
	return json.Unmarshal([]byte(content.Text), destination)
}

func toolError(result *mcp.CallToolResult) string {
	if err := result.GetError(); err != nil {
		return err.Error()
	}
	return "unknown merchant tool error"
}
