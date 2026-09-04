// Package telegram provides the minimal long-polling transport for the buyer.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Update is the subset of Telegram updates used by the buyer commands.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Message contains a text message and its sender identifiers.
type Message struct {
	MessageID       int    `json:"message_id"`
	Chat            Chat   `json:"chat"`
	From            User   `json:"from"`
	Text            string `json:"text"`
	CallbackQueryID string `json:"-"`
}

// Chat identifies a Telegram conversation.
type Chat struct {
	ID int64 `json:"id"`
}

// User identifies a Telegram sender.
type User struct {
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
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
	query := url.Values{"timeout": {"30"}, "allowed_updates": {"[\"message\",\"callback_query\"]"}}
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

// SetWebhook asks Telegram to post updates to endpoint instead of waiting for us
// to ask for them. The secret is echoed back in a header on every delivery, which
// is the only way Telegram can prove a request came from it.
//
// One connection at a time is deliberate. Telegram will otherwise deliver
// concurrently, and out of order arrivals would let a later update advance the
// stored offset past an earlier one that has not been handled yet. The buyer
// handles one message at a time anyway, so nothing is lost by the limit.
func (c *Client) SetWebhook(ctx context.Context, endpoint, secret string) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("telegram webhook endpoint is required")
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("telegram webhook secret is required")
	}
	query := url.Values{
		"url":             {endpoint},
		"secret_token":    {secret},
		"allowed_updates": {`["message","callback_query"]`},
		"max_connections": {"1"},
	}
	var response apiResponse[bool]
	if err := c.call(ctx, "setWebhook", query, nil, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram setWebhook: %s", response.Description)
	}
	return nil
}

// SendMessage sends plain text to a conversation.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.SendMessageWithMarkup(ctx, chatID, text, nil)
}

// SendMessageWithMarkup sends plain text with buttons attached.
func (c *Client) SendMessageWithMarkup(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	return c.send(ctx, chatID, text, "", markup)
}

// SendRich sends text already written as valid markup, so headings and amounts
// read as headings and amounts instead of as one grey paragraph. Interpolate any
// value that did not come from this codebase through Escape first: a product name
// or a merchant's own words containing a bracket would otherwise be rejected by
// the API and the person would see nothing at all.
func (c *Client) SendRich(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	return c.send(ctx, chatID, text, "HTML", markup)
}

// Escape makes an arbitrary string safe to place inside rich text.
func Escape(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// SendChatAction shows that work is happening. Telegram clears the indicator
// after a few seconds on its own, so this is sent again as a run progresses
// rather than once at the start. A failure here costs an animation and nothing
// else, so it is reported to the caller and never worth failing a run over.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	if strings.TrimSpace(action) == "" {
		action = "typing"
	}
	var response apiResponse[json.RawMessage]
	payload := map[string]any{"chat_id": chatID, "action": action}
	if err := c.call(ctx, "sendChatAction", url.Values{}, payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram sendChatAction: %s", response.Description)
	}
	return nil
}

// send posts one message, optionally as rich text and optionally with buttons.
func (c *Client) send(ctx context.Context, chatID int64, text, parseMode string, markup *InlineKeyboardMarkup) error {
	_, err := c.post(ctx, chatID, text, parseMode, markup)
	return err
}

// sentMessage carries back only the identifier a later edit needs.
type sentMessage struct {
	MessageID int `json:"message_id"`
}

// SendTracked sends rich text and reports the message identifier, so a later note
// can replace this message rather than stack another one underneath it.
func (c *Client) SendTracked(ctx context.Context, chatID int64, text string) (int, error) {
	return c.post(ctx, chatID, text, "HTML", nil)
}

// EditTracked replaces the text of a message this bot already sent.
func (c *Client) EditTracked(ctx context.Context, chatID int64, messageID int, text string) error {
	if messageID <= 0 {
		return fmt.Errorf("a message to edit is required")
	}
	payload := map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	var response apiResponse[json.RawMessage]
	if err := c.call(ctx, "editMessageText", url.Values{}, payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram editMessageText: %s", response.Description)
	}
	return nil
}

// post sends one message and reports which message it became.
func (c *Client) post(ctx context.Context, chatID int64, text, parseMode string, markup *InlineKeyboardMarkup) (int, error) {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
		// Amounts and product names are the point of these messages. A link
		// preview unfurling underneath them is not.
		payload["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	var response apiResponse[sentMessage]
	if err := c.call(ctx, "sendMessage", url.Values{}, payload, &response); err != nil {
		return 0, err
	}
	if !response.OK {
		return 0, fmt.Errorf("telegram sendMessage: %s", response.Description)
	}
	return response.Result.MessageID, nil
}

// AnswerCallbackQuery clears the pending spinner on a tapped button. The API
// answers this one with a bare true rather than an object, so the result is
// taken as raw JSON and ignored.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id string) error {
	var response apiResponse[json.RawMessage]
	if err := c.call(ctx, "answerCallbackQuery", url.Values{}, map[string]any{"callback_query_id": id}, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram answer callback failed: %s", response.Description)
	}
	return nil
}

// SendDocument uploads a small text file (e.g. a negotiation transcript)
// to the chat via the multipart sendDocument API.
func (c *Client) SendDocument(ctx context.Context, chatID int64, filename, content string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("write chat_id field: %w", err)
	}
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return fmt.Errorf("create document form file: %w", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		return fmt.Errorf("write document part: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sendDocument", body)
	if err != nil {
		return fmt.Errorf("create sendDocument request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("send document: %w", err)
	}
	defer response.Body.Close()
	var decoded apiResponse[json.RawMessage]
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode sendDocument response: %w", err)
	}
	if response.StatusCode != http.StatusOK || !decoded.OK {
		return fmt.Errorf("telegram sendDocument: %s", decoded.Description)
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
