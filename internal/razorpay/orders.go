// Package razorpay creates server-side order artifacts for wallet purchases.
package razorpay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Order is the server-side artifact returned by the payment API.
type Order struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
}

// Client creates no-capture order artifacts without opening Checkout.
type Client struct {
	keyID     string
	keySecret string
	http      *http.Client
	baseURL   string
}

// NewClient constructs a server-side order client.
func NewClient(keyID, keySecret string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(keyID) == "" || strings.TrimSpace(keySecret) == "" {
		return nil, fmt.Errorf("payment API credentials are required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{keyID: keyID, keySecret: keySecret, http: httpClient, baseURL: "https://api.razorpay.com/v1"}, nil
}

// basicAuth encodes the API credentials for one request.
func (c *Client) basicAuth() string {
	return base64.StdEncoding.EncodeToString([]byte(c.keyID + ":" + c.keySecret))
}

// CreateWalletArtifact creates an unpaid order used only as an audit artifact.
func (c *Client) CreateWalletArtifact(ctx context.Context, amountPaise int64, receipt string, notes map[string]string) (Order, error) {
	if amountPaise <= 0 || strings.TrimSpace(receipt) == "" {
		return Order{}, fmt.Errorf("amount and receipt are required")
	}
	payload, err := json.Marshal(map[string]any{"amount": amountPaise, "currency": "INR", "receipt": receipt, "payment_capture": 0, "notes": notes})
	if err != nil {
		return Order{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/orders", strings.NewReader(string(payload)))
	if err != nil {
		return Order{}, err
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Order{}, fmt.Errorf("create payment order artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Order{}, fmt.Errorf("payment order artifact returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var order Order
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return Order{}, err
	}
	if order.ID == "" {
		return Order{}, fmt.Errorf("payment order artifact returned no id")
	}
	return order, nil
}
