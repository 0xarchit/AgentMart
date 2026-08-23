// Package negotiationclient calls the merchant negotiation boundary.
package negotiationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client sends proposal and resolution messages to the merchant.
type Client struct {
	endpoint string
	http     *http.Client
}

// New constructs a merchant negotiation client.
func New(endpoint string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("merchant negotiation endpoint is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: httpClient}, nil
}

// Proposal is the merchant's counter offer for a product quantity.
type Proposal struct {
	SessionID        string `json:"session_id"`
	Type             string `json:"type"`
	ProductID        string `json:"product_id"`
	Quantity         int    `json:"qty"`
	BaseAmountPaise  int64  `json:"base_amount_paise"`
	FinalAmountPaise int64  `json:"final_amount_paise"`
	Reason           string `json:"reason"`
}

// Resolution is the final merchant negotiation state.
type Resolution struct {
	SessionID        string `json:"session_id"`
	Status           string `json:"status"`
	ProductID        string `json:"product_id"`
	Quantity         int    `json:"qty"`
	BaseAmountPaise  int64  `json:"base_amount_paise"`
	FinalAmountPaise int64  `json:"final_amount_paise"`
	UpliftPaise      int64  `json:"uplift_paise"`
}

// Propose asks the merchant for a counter offer.
func (c *Client) Propose(ctx context.Context, productID string, quantity int) (Proposal, error) {
	var result Proposal
	err := c.post(ctx, map[string]any{"type": "propose", "product_id": productID, "qty": quantity}, &result)
	return result, err
}

// Accept accepts a merchant counter offer.
func (c *Client) Accept(ctx context.Context, sessionID string) (Resolution, error) {
	return c.resolve(ctx, sessionID, "accept", "")
}

// Decline declines a merchant counter offer.
func (c *Client) Decline(ctx context.Context, sessionID, reason string) (Resolution, error) {
	return c.resolve(ctx, sessionID, "decline", reason)
}

func (c *Client) resolve(ctx context.Context, sessionID, kind, reason string) (Resolution, error) {
	var result Resolution
	err := c.post(ctx, map[string]any{"type": kind, "session_id": sessionID, "reason": reason}, &result)
	return result, err
}

func (c *Client) post(ctx context.Context, payload any, destination any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode negotiation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create negotiation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("negotiation request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if failure.Error == "" {
			failure.Error = resp.Status
		}
		return fmt.Errorf("merchant negotiation failed: %s", failure.Error)
	}
	if err := json.NewDecoder(resp.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode negotiation response: %w", err)
	}
	return nil
}
