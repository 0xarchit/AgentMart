// Package negotiationclient calls the merchant negotiation boundary.
package negotiationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agentmart/internal/negotiation"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// Client sends proposal and resolution messages to the merchant.
type Client struct {
	endpoint string
	http     *http.Client
	a2a      *a2aclient.Client
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

// NewA2A discovers a merchant agent card and creates a standards-based client.
func NewA2A(ctx context.Context, endpoint string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("merchant agent endpoint is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resolver := agentcard.NewResolver(httpClient)
	card, err := resolver.Resolve(ctx, strings.TrimRight(endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("resolve merchant agent card: %w", err)
	}
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create merchant agent client: %w", err)
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: httpClient, a2a: client}, nil
}

// Close releases transport resources held by the merchant agent client.
func (c *Client) Close() error {
	if c.a2a == nil {
		return nil
	}
	return c.a2a.Destroy()
}

// Proposal is the merchant's counter offer for a product quantity.
type Proposal struct {
	SessionID        string             `json:"session_id"`
	Type             string             `json:"type"`
	ProductID        string             `json:"product_id"`
	Quantity         int                `json:"qty"`
	BaseAmountPaise  int64              `json:"base_amount_paise"`
	FinalAmountPaise int64              `json:"final_amount_paise"`
	Reason           string             `json:"reason"`
	OfferReason      string             `json:"offer_reason,omitempty"`
	Name             string             `json:"name,omitempty"`
	Category         string             `json:"category,omitempty"`
	Stock            int                `json:"stock,omitempty"`
	WarrantyYears    int                `json:"warranty_years,omitempty"`
	TrustScore       int                `json:"trust_score,omitempty"`
	ComboWith        string             `json:"combo_with,omitempty"`
	ComboDiscountPct int                `json:"combo_discount_pct,omitempty"`
	Transcript       []negotiation.Turn `json:"transcript,omitempty"`
}

// Resolution is the final merchant negotiation state.
type Resolution struct {
	SessionID        string             `json:"session_id"`
	Status           string             `json:"status"`
	ProductID        string             `json:"product_id"`
	Quantity         int                `json:"qty"`
	BaseAmountPaise  int64              `json:"base_amount_paise"`
	FinalAmountPaise int64              `json:"final_amount_paise"`
	UpliftPaise      int64              `json:"uplift_paise"`
	Transcript       []negotiation.Turn `json:"transcript,omitempty"`
}

// Counter submits a buyer counter amount against an open session. The merchant
// may accept (status accepted), re-counter (status countered), or decline.
func (c *Client) Counter(ctx context.Context, sessionID string, amountPaise int64) (Resolution, error) {
	payload := map[string]any{"type": "counter", "session_id": sessionID, "counter_amount_paise": amountPaise}
	if c.a2a != nil {
		return c.a2aResolution(ctx, payload)
	}
	var result Resolution
	err := c.post(ctx, payload, &result)
	return result, err
}

// Propose asks the merchant for a counter offer.
func (c *Client) Propose(ctx context.Context, productID string, quantity int) (Proposal, error) {
	return c.ProposeAs(ctx, productID, quantity, "")
}

// ProposeAs asks the merchant for a counter offer while identifying the buyer
// account, which lets the merchant agent apply campaign and loyalty deals.
func (c *Client) ProposeAs(ctx context.Context, productID string, quantity int, accountID string) (Proposal, error) {
	payload := map[string]any{"type": "propose", "product_id": productID, "qty": quantity}
	if strings.TrimSpace(accountID) != "" {
		payload["account_id"] = accountID
	}
	if c.a2a != nil {
		return c.a2aProposal(ctx, payload)
	}
	var result Proposal
	err := c.post(ctx, payload, &result)
	return result, err
}

// Shortlist is the merchant's answer to "what do you have": its own pick of
// stock, pitched in the owner's words, with prices from the shop's records.
type Shortlist struct {
	Greeting   string             `json:"greeting"`
	Options    []ShortlistOption  `json:"options"`
	Closing    string             `json:"closing,omitempty"`
	Transcript []negotiation.Turn `json:"transcript,omitempty"`
}

// ShortlistOption is one pitched product from the merchant.
type ShortlistOption struct {
	ProductID     string `json:"product_id"`
	Name          string `json:"name"`
	PricePaise    int64  `json:"price_paise"`
	Pitch         string `json:"pitch"`
	Includes      string `json:"includes,omitempty"`
	Stock         int    `json:"stock,omitempty"`
	WarrantyYears int    `json:"warranty_years,omitempty"`
	TrustScore    int    `json:"trust_score,omitempty"`
}

// Browse opens the conversation: the buyer tells the merchant what it is after
// and the merchant answers with what it wants to show.
func (c *Client) Browse(ctx context.Context, brief string, budgetPaise int64, accountID string) (Shortlist, error) {
	payload := map[string]any{"type": "browse", "brief": brief}
	if budgetPaise > 0 {
		payload["budget_paise"] = budgetPaise
	}
	if strings.TrimSpace(accountID) != "" {
		payload["account_id"] = accountID
	}
	if c.a2a != nil {
		return c.a2aShortlist(ctx, payload)
	}
	var result Shortlist
	err := c.post(ctx, payload, &result)
	return result, err
}

func (c *Client) a2aShortlist(ctx context.Context, request map[string]any) (Shortlist, error) {
	var result Shortlist
	text, err := c.sendA2A(ctx, request)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return result, fmt.Errorf("decode merchant shortlist: %w", err)
	}
	return result, nil
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
	if c.a2a != nil {
		result, err := c.a2aResolution(ctx, map[string]any{"type": kind, "session_id": sessionID, "reason": reason})
		return result, err
	}
	var result Resolution
	err := c.post(ctx, map[string]any{"type": kind, "session_id": sessionID, "reason": reason}, &result)
	return result, err
}

func (c *Client) a2aProposal(ctx context.Context, request map[string]any) (Proposal, error) {
	var result Proposal
	payload, err := c.sendA2A(ctx, request)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return result, fmt.Errorf("decode merchant proposal: %w", err)
	}
	return result, nil
}

func (c *Client) a2aResolution(ctx context.Context, request map[string]any) (Resolution, error) {
	var result Resolution
	payload, err := c.sendA2A(ctx, request)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return result, fmt.Errorf("decode merchant resolution: %w", err)
	}
	return result, nil
}

func (c *Client) sendA2A(ctx context.Context, request map[string]any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode merchant request: %w", err)
	}
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(string(payload)))
	result, err := c.a2a.SendMessage(ctx, &a2a.SendMessageRequest{Message: message})
	if err != nil {
		return "", fmt.Errorf("send merchant request: %w", err)
	}
	return extractA2AText(result)
}

func extractA2AText(result a2a.SendMessageResult) (string, error) {
	var parts []*a2a.Part
	switch value := result.(type) {
	case *a2a.Message:
		parts = value.Parts
	case *a2a.Task:
		for _, artifact := range value.Artifacts {
			parts = append(parts, artifact.Parts...)
		}
		if len(parts) == 0 {
			for _, message := range value.History {
				parts = append(parts, message.Parts...)
			}
		}
	default:
		return "", fmt.Errorf("merchant agent returned unsupported result %T", result)
	}
	for _, part := range parts {
		if text := part.Text(); text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("merchant agent response contained no text artifact")
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
