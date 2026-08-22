// Package telegram provides the minimal long-polling transport for the buyer.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Update is the subset of Telegram updates used by the buyer commands.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message contains a text message and its sender identifiers.
type Message struct {
	MessageID int    `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
	Text      string `json:"text"`
}

// Chat identifies a Telegram conversation.
type Chat struct {
	ID int64 `json:"id"`
}

// User identifies a Telegram sender.
type User struct {
	ID int64 `json:"id"`
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
}

// Client calls the Telegram Bot API.
type Client struct {
	token   string
	http    *http.Client
	baseURL string
}

// NewClient constructs a Telegram API client.
func NewClient(token string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("telegram token is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 35 * time.Second}
	}
	return &Client{token: token, http: httpClient, baseURL: "https://api.telegram.org/bot" + token}, nil
}

// Poll retrieves updates after the supplied offset using long polling.
func (c *Client) Poll(ctx context.Context, offset int) ([]Update, error) {
	query := url.Values{"timeout": {"30"}, "allowed_updates": {"[\"message\"]"}}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	var response apiResponse[[]Update]
	if err := c.call(ctx, "getUpdates", query, nil, &response); err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", response.Description)
	}
	return response.Result, nil
}

// SendMessage sends plain text to a conversation.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	query := url.Values{}
	payload := map[string]any{"chat_id": chatID, "text": text}
	var response apiResponse[json.RawMessage]
	if err := c.call(ctx, "sendMessage", query, payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram sendMessage: %s", response.Description)
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, query url.Values, payload any, dst any) error {
	endpoint := c.baseURL + "/" + method
	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+queryString(query), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func queryString(query url.Values) string {
	if encoded := query.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}
