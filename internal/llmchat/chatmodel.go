// Package llmchat implements adk's model.LLM over the universally-supported
// OpenAI POST {base}/chat/completions wire format with function calling.
//
// Why this exists: ADK v2.2.0's openaimodel hardcodes OpenAI's Responses API
// (POST {base}/responses), which OpenRouter free pools, NVIDIA NIM, OpenCode
// Zen (for non-GPT families), and most gateways reject.
package llmchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// providerTimeout bounds each HTTP call so one hung endpoint cannot eat an
// entire agent-run budget. Free-tier reasoning models can take 30-90s before
// their first byte.
const providerTimeout = 120 * time.Second

// Model speaks /chat/completions for any OpenAI-compatible provider.
type Model struct {
	name    string
	apiKey  string
	baseURL string
	http    *http.Client
}

// New builds a chat-completions-backed adk model.LLM.
func New(name, apiKey, baseURL string) *Model {
	return &Model{name: name, apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: providerTimeout}}
}

func (m *Model) Name() string { return m.name }

type request struct {
	Model      string         `json:"model"`
	Messages   []message      `json:"messages"`
	Tools      []tool         `json:"tools,omitempty"`
	ToolChoice map[string]any `json:"tool_choice,omitempty"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string | nil on assistant tool_call turns
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"` // some gateways want it on tool turns
}

type toolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content   *string    `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateContent implements adk model.LLM. Non-streaming by design: one
// complete response per call (the runner accepts single-event iterators).
func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if stream {
			yield(nil, fmt.Errorf("llmchat: streaming unsupported"))
			return
		}
		body, err := m.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		decoded, err := m.post(ctx, body)
		if err != nil {
			yield(nil, err)
			return
		}
		response := m.adapt(decoded)
		yield(response, nil)
	}
}

func (m *Model) post(ctx context.Context, body []byte) (*response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llmchat: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, postErr := m.http.Do(httpReq)
	if postErr != nil || (resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests) {
		status := "transport error"
		if postErr == nil {
			status = fmt.Sprintf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
		}
		// Free-tier providers hiccup constantly; one bounded retry.
		time.Sleep(2 * time.Second)
		retryReq, rerr := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
		if rerr != nil {
			return nil, fmt.Errorf("llmchat: rebuild retry request: %w", rerr)
		}
		retryReq.Header.Set("Authorization", "Bearer "+m.apiKey)
		retryReq.Header.Set("Content-Type", "application/json")
		resp, postErr = m.http.Do(retryReq)
		if postErr != nil {
			return nil, fmt.Errorf("llmchat: call failed twice (%s then %v)", status, postErr)
		}
	}
	defer resp.Body.Close()

	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("llmchat: decode response (%s): %w", resp.Status, err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("llmchat: provider error (%s): %s", resp.Status, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("llmchat: no choices returned (%s)", resp.Status)
	}
	return &decoded, nil
}

// adapt converts the provider choice into an ADK LLMResponse. Tool results
// flow back through GenerateContent requests built by this same adapter.
func (m *Model) adapt(decoded *response) *model.LLMResponse {
	out := &model.LLMResponse{
		Content:      &genai.Content{Role: "model"},
		TurnComplete: true,
	}
	choice := decoded.Choices[0]
	if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" {
		out.Content.Parts = append(out.Content.Parts, &genai.Part{Text: *choice.Message.Content})
	}
	for i, call := range choice.Message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				out.ErrorMessage = fmt.Sprintf("decode arguments for %s: %v", call.Function.Name, err)
				return out
			}
		}
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out.Content.Parts = append(out.Content.Parts,
			&genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: call.Function.Name, Args: args}})
	}
	return out
}

// buildRequest converts the ADK conversation into chat-completions messages.
//
// ADK delivers tool results as genai.Contents whose Parts carry
// FunctionResponse (role is typically "user"), and model turns carry
// FunctionCall parts (role "model"). Both MUST reach the provider: results as
// role:"tool" messages echoing the call ID, calls inside assistant messages.
func (m *Model) buildRequest(req *model.LLMRequest) ([]byte, error) {
	out := request{Model: req.Model}
	if req.Config != nil {
		for _, declared := range req.Config.Tools {
			for _, decl := range declared.FunctionDeclarations {
				out.Tools = append(out.Tools, tool{Type: "function", Function: toolFunction{
					Name: decl.Name, Description: decl.Description,
					Parameters: schemaAsMap(decl.Parameters),
				}})
			}
		}
	}

	tracker := newCallTracker()
	for _, content := range req.Contents {
		if content == nil || len(content.Parts) == 0 {
			continue
		}
		isModel := content.Role == "model"
		var textParts []string
		var calls []toolCall
		var results []message

		for _, part := range content.Parts {
			switch {
			case part == nil:
				continue
			case part.Text != "":
				textParts = append(textParts, part.Text)
			case part.FunctionCall != nil:
				calls = append(calls, tracker.call(part.FunctionCall))
			case part.FunctionResponse != nil:
				payload := part.FunctionResponse.Response
				if payload == nil {
					payload = map[string]any{}
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					return nil, fmt.Errorf("llmchat: encode function response: %w", err)
				}
				results = append(results, message{
					Role: "tool", ToolCallID: tracker.responseID(part.FunctionResponse),
					Name: part.FunctionResponse.Name, Content: string(encoded),
				})
			}
		}

		switch {
		case len(textParts) > 0 && len(calls) == 0:
			role := "user"
			if isModel {
				role = "assistant"
			}
			out.Messages = append(out.Messages, message{Role: role, Content: strings.Join(textParts, "\n")})
		case len(calls) > 0:
			msg := message{Role: "assistant", ToolCalls: calls}
			if len(textParts) > 0 {
				msg.Content = strings.Join(textParts, "\n")
			}
			out.Messages = append(out.Messages, msg)
		default:
			// tool-results-only or empty text under another role
			for _, result := range results {
				out.Messages = append(out.Messages, result)
			}
			if len(textParts) > 0 {
				out.Messages = append(out.Messages, message{Role: "user", Content: strings.Join(textParts, "\n")})
			}
			continue
		}
		out.Messages = append(out.Messages, results...)
	}
	return json.Marshal(out)
}

// callTracker issues IDs for tool calls lacking one and remembers them so the
// matching FunctionResponse (which often carries only the function name) can
// echo the right tool_call_id back to the provider.
type callTracker struct {
	seq     int
	lastFor map[string]string
}

func newCallTracker() *callTracker { return &callTracker{lastFor: map[string]string{}} }

func (t *callTracker) call(fc *genai.FunctionCall) toolCall {
	t.seq++
	id := fc.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", t.seq)
	}
	argsJSON := "{}"
	if len(fc.Args) > 0 {
		if encoded, err := json.Marshal(fc.Args); err == nil {
			argsJSON = string(encoded)
		}
	}
	t.lastFor[fc.Name] = id
	return toolCall{ID: id, Type: "function", Function: function{Name: fc.Name, Arguments: argsJSON}}
}

func (t *callTracker) responseID(fr *genai.FunctionResponse) string {
	if fr.ID != "" {
		return fr.ID
	}
	return t.lastFor[fr.Name]
}

// schemaAsMap converts a genai.Schema into plain OpenAI-style JSON schema,
// lowering Gemini-style enum types ("OBJECT") to OpenAI conventions ("object").
func schemaAsMap(schema *genai.Schema) map[string]any {
	if schema == nil {
		return nil
	}
	out := map[string]any{}
	if schema.Type != "" {
		out["type"] = strings.ToLower(string(schema.Type))
	}
	if schema.Description != "" {
		out["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		out["enum"] = schema.Enum
	}
	if len(schema.Required) > 0 {
		out["required"] = schema.Required
	}
	if len(schema.Properties) > 0 {
		properties := make(map[string]any, len(schema.Properties))
		for name, child := range schema.Properties {
			properties[name] = schemaAsMap(child)
		}
		out["properties"] = properties
	}
	if schema.Items != nil {
		out["items"] = schemaAsMap(schema.Items)
	}
	return out
}
