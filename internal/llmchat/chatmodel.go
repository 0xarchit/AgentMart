// Package llmchat implements the framework's model interface over the universally-supported
// OpenAI POST {base}/chat/completions wire format with function calling.
//
// Why this exists: the framework's bundled OpenAI model hardcodes OpenAI's Responses API
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
	"strconv"
	"strings"
	"time"

	"agentmart/internal/failure"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// providerTimeout bounds each HTTP call so one hung endpoint cannot eat an
// entire agent-run budget. Free-tier reasoning models can take 30-90s before
// their first byte.
const providerTimeout = 120 * time.Second

// Retry budget for a shared free pool. A rate limit there usually clears in a
// second or two, so the first retry is quick and each later one waits longer.
// The provider's own stated wait wins when it sends one.
const (
	maxAttempts  = 5
	retryBase    = time.Second
	maxRetryWait = 20 * time.Second
)

// structuredOutputTool is the function a schema-shaped answer arrives through.
// Function calling is the one structured-output mechanism every gateway in this
// project supports; json_schema response formats are not.
const structuredOutputTool = "final_answer"

// wantsStructuredOutput reports whether the caller asked for a schema-shaped
// answer rather than prose.
func wantsStructuredOutput(req *model.LLMRequest) bool {
	return req != nil && req.Config != nil && req.Config.ResponseSchema != nil
}

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
			yield(nil, failure.Reasoning(fmt.Errorf("streaming unsupported")))
			return
		}
		body, err := m.buildRequest(req)
		if err != nil {
			yield(nil, failure.Reasoning(err))
			return
		}
		decoded, err := m.post(ctx, body)
		if err != nil {
			yield(nil, failure.Reasoning(err))
			return
		}
		response := m.adapt(decoded, wantsStructuredOutput(req))
		yield(response, nil)
	}
}

// post calls the provider, retrying the failures a shared free pool produces
// constantly: rate limits, gateway errors, and dropped connections. It waits as
// long as the provider asks when it says so, and backs off otherwise.
func (m *Model) post(ctx context.Context, body []byte) (*response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		decoded, wait, err := m.attempt(ctx, body)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
		if wait <= 0 || attempt == maxAttempts {
			break
		}
		// 1s, 2s, 4s, 8s unless the provider named its own wait.
		wait *= 1 << (attempt - 1)
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, fmt.Errorf("%d attempts failed, last was: %w", maxAttempts, lastErr)
}

// attempt makes one call. A positive wait means the failure is worth retrying
// after that long; zero means stop.
func (m *Model) attempt(ctx context.Context, body []byte) (*response, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(httpReq)
	if err != nil {
		return nil, retryBase, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
		return nil, retryAfter(resp), fmt.Errorf("provider returned %s", resp.Status)
	}

	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, retryBase, fmt.Errorf("decode response (%s): %w", resp.Status, err)
	}
	if decoded.Error != nil {
		return nil, 0, fmt.Errorf("provider error (%s): %s", resp.Status, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		// An empty body with a success status is a pool hiccup, not an answer.
		return nil, retryBase, fmt.Errorf("no choices returned (%s)", resp.Status)
	}
	return &decoded, 0, nil
}

// retryAfter honours the provider's own wait when it states one, and otherwise
// backs off by a fixed step so a rate-limited pool gets time to reset.
func retryAfter(resp *http.Response) time.Duration {
	header := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
			if wait := time.Duration(seconds) * time.Second; wait <= maxRetryWait {
				return wait
			}
			return maxRetryWait
		}
	}
	return retryBase
}

// adapt converts the provider choice into a framework response. Tool results
// flow back through GenerateContent requests built by this same adapter.
func (m *Model) adapt(decoded *response, structured bool) *model.LLMResponse {
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
		// The structured-answer call is the reply itself, not a tool to run: hand
		// its arguments back as the model's text so the caller can parse them
		// against the schema it asked for.
		if structured && call.Function.Name == structuredOutputTool {
			encoded, err := json.Marshal(args)
			if err != nil {
				out.ErrorMessage = fmt.Sprintf("encode structured answer: %v", err)
				return out
			}
			out.Content.Parts = []*genai.Part{{Text: string(encoded)}}
			return out
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

// buildRequest converts the framework conversation into chat-completions messages.
//
// The framework delivers tool results as genai.Contents whose Parts carry
// FunctionResponse (role is typically "user"), and model turns carry
// FunctionCall parts (role "model"). Both MUST reach the provider: results as
// role:"tool" messages echoing the call ID, calls inside assistant messages.
func (m *Model) buildRequest(req *model.LLMRequest) ([]byte, error) {
	out := request{Model: req.Model}
	if req.Config != nil {
		// The agent's instruction arrives here, not in the conversation. Dropping
		// it leaves the model with no role and no output contract, so it answers
		// the raw user text as an open-ended chat.
		if instruction := textOf(req.Config.SystemInstruction); instruction != "" {
			out.Messages = append(out.Messages, message{Role: "system", Content: instruction})
		}
		for _, declared := range req.Config.Tools {
			for _, decl := range declared.FunctionDeclarations {
				out.Tools = append(out.Tools, tool{Type: "function", Function: toolFunction{
					Name: decl.Name, Description: decl.Description,
					Parameters: schemaAsMap(decl.Parameters),
				}})
			}
		}
	}

	// A caller that asked for a schema-shaped answer gets one: the schema is
	// declared as a function the model answers through. Chat models otherwise
	// reply in prose, which then fails the caller's own validation.
	if wantsStructuredOutput(req) {
		answer := tool{Type: "function", Function: toolFunction{
			Name:        structuredOutputTool,
			Description: "Give your final answer through this function. Every field is required.",
			Parameters:  schemaAsMap(req.Config.ResponseSchema),
		}}
		hadOtherTools := len(out.Tools) > 0
		out.Tools = append(out.Tools, answer)
		if !hadOtherTools {
			// Nothing else to call, so the answer is the only legal move.
			out.ToolChoice = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": structuredOutputTool},
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
					return nil, fmt.Errorf("encode function response: %w", err)
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

// textOf flattens a content block into plain text.
func textOf(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var parts []string
	for _, part := range content.Parts {
		if part != nil && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n\n")
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
