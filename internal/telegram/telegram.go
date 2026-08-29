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

// SendMessage sends plain text to a conversation.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.SendMessageWithMarkup(ctx, chatID, text, nil)
}

func (c *Client) SendMessageWithMarkup(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	query := url.Values{}
	payload := map[string]any{"chat_id": chatID, "text": text}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	var response apiResponse[json.RawMessage]
	if err := c.call(ctx, "sendMessage", query, payload, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("telegram sendMessage: %s", response.Description)
	}
	return nil
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
