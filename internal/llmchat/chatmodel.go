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
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	// attemptsPerFallback is used when a model chain is configured: fewer waits
	// per model, because moving to a working model beats waiting on a dead one.
	attemptsPerFallback = 2
	// maxBodySnippet bounds how much of a failing response is quoted back.
	maxBodySnippet = 600
	// allowanceCooldown keeps a model whose free allowance is spent out of the
	// chain for a while. That refusal does not clear in seconds, so retrying it
	// or re-probing it on the next stage spends requests that cannot succeed.
	allowanceCooldown = 10 * time.Minute
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
	models  []string
	apiKey  string
	baseURL string
	http    *http.Client

	mu        sync.Mutex
	coolUntil map[string]time.Time
}

// New builds a chat-completions-backed model. Name may be a comma-separated
// chain, for example "laguna-s-2.1-free,hy3-free". A free pool takes models
// offline without warning, so when the first is unavailable the next is tried
// rather than failing the run.
func New(name, apiKey, baseURL string) *Model {
	var models []string
	for _, candidate := range strings.Split(name, ",") {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			models = append(models, trimmed)
		}
	}
	if len(models) == 0 {
		models = []string{strings.TrimSpace(name)}
	}
	return &Model{
		name:      models[0],
		models:    models,
		apiKey:    apiKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: providerTimeout},
		coolUntil: map[string]time.Time{},
	}
}

// allowanceSpent reports a refusal that no retry can clear: the free allowance
// or credit for that model is gone until the provider's own window resets.
func allowanceSpent(detail string) bool {
	lowered := strings.ToLower(detail)
	for _, marker := range []string{"freeusagelimit", "usage limit", "quota", "credit", "insufficient_", "billing"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// benchModel puts one model out of the chain until its allowance window passes.
func (m *Model) benchModel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.coolUntil == nil {
		m.coolUntil = map[string]time.Time{}
	}
	m.coolUntil[name] = time.Now().Add(allowanceCooldown)
}

// callable lists the models worth calling now. When every model is benched they
// are all returned anyway, so a spent pool degrades to the old behaviour rather
// than refusing to call anything at all.
func (m *Model) callable() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	ready := make([]string, 0, len(m.models))
	for _, name := range m.models {
		if until, benched := m.coolUntil[name]; benched && now.Before(until) {
			continue
		}
		ready = append(ready, name)
	}
	if len(ready) == 0 {
		return m.models
	}
	return ready
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
		body, err := m.buildRequest(req, false)
		if err != nil {
			yield(nil, failure.Reasoning(err))
			return
		}
		decoded, err := m.post(ctx, body)
		if err != nil {
			yield(nil, failure.Reasoning(err))
			return
		}
		structured := wantsStructuredOutput(req)
		response := m.adapt(decoded, structured)
		// A stage that asked for a shaped answer has nothing it can use without
		// one. Prose or an empty turn both happen when the model also had real
		// tools to call, so the answer function could not be the only legal move
		// on the first ask. Ask once more with the answer as the only move left.
		if structured && answeredWithoutTheShape(response) {
			forced, buildErr := m.buildRequest(req, true)
			if buildErr != nil {
				yield(nil, failure.Reasoning(buildErr))
				return
			}
			if redecoded, postErr := m.post(ctx, forced); postErr == nil {
				response = m.adapt(redecoded, true)
			}
		}
		yield(response, nil)
	}
}

// post works down the model chain, skipping any model whose free allowance is
// known to be spent. Each model gets a few bounded attempts for the transient
// failures a shared pool produces constantly; when a model is simply
// unavailable or out of free quota, the next one is tried.
func (m *Model) post(ctx context.Context, body []byte) (*response, error) {
	attempts := maxAttempts
	if len(m.models) > 1 {
		// With somewhere else to go, spend less time waiting on one endpoint.
		attempts = attemptsPerFallback
	}
	var reasons []string
	for _, name := range m.callable() {
		aimed, err := withModel(body, name)
		if err != nil {
			return nil, err
		}
		decoded, err := m.postTo(ctx, aimed, attempts)
		if err == nil {
			return decoded, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		if allowanceSpent(err.Error()) {
			m.benchModel(name)
		}
		reasons = append(reasons, name+" -> "+err.Error())
	}
	if len(reasons) == 1 {
		return nil, fmt.Errorf("%s", reasons[0])
	}
	return nil, fmt.Errorf("every configured model failed: %s", strings.Join(reasons, "; "))
}

// withModel aims an already-encoded request at one model.
func withModel(body []byte, name string) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("reaim request: %w", err)
	}
	aimed, err := json.Marshal(name)
	if err != nil {
		return nil, fmt.Errorf("encode model name: %w", err)
	}
	fields["model"] = aimed
	return json.Marshal(fields)
}

// postTo calls one model, retrying the failures that clear on their own. A rate
// limit on a shared pool usually clears in a second or two, so the first retry
// is quick and each later one waits longer.
func (m *Model) postTo(ctx context.Context, body []byte, attempts int) (*response, error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		decoded, wait, err := m.attempt(ctx, body)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
		if wait <= 0 || attempt == attempts {
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
	return nil, lastErr
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
		// The body is where the provider says which model, key, or field it
		// objected to. Discarding it leaves nothing to act on.
		detail := bodySnippet(resp)
		wait := retryAfter(resp)
		if allowanceSpent(detail) {
			// A spent allowance does not clear in seconds. Waiting on it only
			// spends requests that cannot succeed, so move on instead.
			wait = 0
		}
		return nil, wait, fmt.Errorf("provider returned %s: %s", resp.Status, detail)
	}
	if resp.StatusCode >= 400 {
		// A refusal aimed at this model, such as an unsupported request shape or a
		// model the key cannot reach. Another model may accept it.
		return nil, 0, fmt.Errorf("provider refused with %s: %s", resp.Status, bodySnippet(resp))
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

// bodySnippet reads a bounded piece of a failing response so the reason travels
// with the error without dragging a whole page into a log line.
func bodySnippet(resp *http.Response) string {
	limited, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySnippet))
	if err != nil || len(limited) == 0 {
		return "no detail in the response body"
	}
	return strings.Join(strings.Fields(string(limited)), " ")
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

// answeredWithoutTheShape reports that a shaped answer was asked for and did not
// arrive: no function call to run, and no text carrying the shape. An empty turn
// counts, since a stage waiting on a shape cannot use silence either.
func answeredWithoutTheShape(response *model.LLMResponse) bool {
	if response == nil || response.ErrorMessage != "" || response.Content == nil {
		return false
	}
	text := ""
	for _, part := range response.Content.Parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil {
			// Mid-conversation tool use, which is a legitimate turn.
			return false
		}
		text += part.Text
	}
	shaped := map[string]any{}
	return json.Unmarshal([]byte(strings.TrimSpace(text)), &shaped) != nil
}

// buildRequest converts the framework conversation into chat-completions
// messages. When forceAnswer is set, the shaped answer is the only move the
// model is allowed, used to recover a turn that came back as prose.
//
// The framework delivers tool results as genai.Contents whose Parts carry
// FunctionResponse (role is typically "user"), and model turns carry
// FunctionCall parts (role "model"). Both MUST reach the provider: results as
// role:"tool" messages echoing the call ID, calls inside assistant messages.
func (m *Model) buildRequest(req *model.LLMRequest, forceAnswer bool) ([]byte, error) {
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
		if !hadOtherTools || forceAnswer {
			// Nothing else worth calling, so the answer is the only legal move.
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
