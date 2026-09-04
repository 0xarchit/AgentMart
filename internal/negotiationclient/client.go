// Package negotiationclient calls the merchant negotiation boundary.
package negotiationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentmart/internal/negotiation"
	"agentmart/internal/runid"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// Client sends proposal and resolution messages to the merchant.
type Client struct {
	endpoint string
	http     *http.Client
	agent    *a2aclient.Client
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

// NewAgentClient discovers a merchant agent card and creates a client that
// talks to the merchant as an agent rather than over the plain endpoint.
func NewAgentClient(ctx context.Context, endpoint string, httpClient *http.Client) (*Client, error) {
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
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: httpClient, agent: client}, nil
}

// Close releases transport resources held by the merchant agent client.
func (c *Client) Close() error {
	if c.agent == nil {
		return nil
	}
	return c.agent.Destroy()
}

// BundledItem is a cross-sell attachment the merchant included in its ask, at
// the discounted price the ask actually charges for it.
type BundledItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PricePaise  int64  `json:"price_paise"`
	DiscountPct int    `json:"discount_pct"`
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
	Bundle           *BundledItem       `json:"bundle,omitempty"`
	Transcript       []negotiation.Turn `json:"transcript,omitempty"`
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
	// BundledPaise is the attached goods already inside FinalAmountPaise. The buyer
	// records it with the sale so the premium is measured over the whole basket.
	BundledPaise int64              `json:"bundled_amount_paise"`
	Transcript   []negotiation.Turn `json:"transcript,omitempty"`
	// QuotedAt is when the shop says this amount became its standing ask. It is
	// carried so a purchase built from a stored session can be dated: the gate
	// refuses a negotiated amount it cannot age.
	QuotedAt time.Time `json:"quoted_at"`
}

// withRun names the run this message belongs to, so the shop's trail rows join
// the buyer's. Outside a run the field is left off entirely.
func withRun(ctx context.Context, payload map[string]any) map[string]any {
	if id := runid.From(ctx); id != "" {
		payload["run_id"] = id
	}
	return payload
}

// Counter submits a buyer counter amount against an open session. The merchant
// may accept (status accepted), re-counter (status countered), or decline.
func (c *Client) Counter(ctx context.Context, sessionID string, amountPaise int64) (Resolution, error) {
	payload := withRun(ctx, map[string]any{"type": "counter", "session_id": sessionID, "counter_amount_paise": amountPaise})
	if c.agent != nil {
		return c.agentResolution(ctx, payload)
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
	payload := withRun(ctx, map[string]any{"type": "propose", "product_id": productID, "qty": quantity})
	if strings.TrimSpace(accountID) != "" {
		payload["account_id"] = accountID
	}
	if c.agent != nil {
		return c.agentProposal(ctx, payload)
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
	payload := withRun(ctx, map[string]any{"type": "browse", "brief": brief})
	if budgetPaise > 0 {
		payload["budget_paise"] = budgetPaise
	}
	if strings.TrimSpace(accountID) != "" {
		payload["account_id"] = accountID
	}
	if c.agent != nil {
		return c.agentShortlist(ctx, payload)
	}
	var result Shortlist
	err := c.post(ctx, payload, &result)
	return result, err
}

func (c *Client) agentShortlist(ctx context.Context, request map[string]any) (Shortlist, error) {
	var result Shortlist
	text, err := c.sendToAgent(ctx, request)
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
	if c.agent != nil {
		result, err := c.agentResolution(ctx, map[string]any{"type": kind, "session_id": sessionID, "reason": reason})
		return result, err
	}
	var result Resolution
	err := c.post(ctx, map[string]any{"type": kind, "session_id": sessionID, "reason": reason}, &result)
	return result, err
}

func (c *Client) agentProposal(ctx context.Context, request map[string]any) (Proposal, error) {
	var result Proposal
	payload, err := c.sendToAgent(ctx, request)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return result, fmt.Errorf("decode merchant proposal: %w", err)
	}
	return result, nil
}

func (c *Client) agentResolution(ctx context.Context, request map[string]any) (Resolution, error) {
	var result Resolution
	payload, err := c.sendToAgent(ctx, request)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return result, fmt.Errorf("decode merchant resolution: %w", err)
	}
	return result, nil
}

func (c *Client) sendToAgent(ctx context.Context, request map[string]any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode merchant request: %w", err)
	}
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(string(payload)))
	result, err := c.agent.SendMessage(ctx, &a2a.SendMessageRequest{Message: message})
	if err != nil {
		return "", fmt.Errorf("send merchant request: %w", err)
	}
	return extractAgentText(result)
}

func extractAgentText(result a2a.SendMessageResult) (string, error) {
	var parts []*a2a.Part
	switch value := result.(type) {
	case *a2a.Message:
		parts = value.Parts
	case *a2a.Task:
		// A failed task carries the merchant's reason in its status, not in an
		// artifact. Reading the history instead would replay our own request,
		// which decodes as a valid empty answer and hides the failure.
		if value.Status.State == a2a.TaskStateFailed {
			return "", fmt.Errorf("merchant could not answer: %s", statusText(value.Status))
		}
		for _, artifact := range value.Artifacts {
			parts = append(parts, artifact.Parts...)
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

// statusText is the merchant's own words about why a task ended as it did.
func statusText(status a2a.TaskStatus) string {
	if status.Message == nil {
		return "no reason given"
	}
	for _, part := range status.Message.Parts {
		if text := part.Text(); text != "" {
			return text
		}
	}
	return "no reason given"
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
