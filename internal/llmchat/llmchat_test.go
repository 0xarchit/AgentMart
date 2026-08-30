// Tests for the structured-answer path: a caller that asks for a result shape
// must get JSON in that shape, not prose.
package llmchat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type probeAnswer struct {
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
	Keywords []string `json:"keywords"`
	Notes    []string `json:"notes,omitempty"`
}

func TestSchemaForDerivesResultShape(t *testing.T) {
	schema, err := SchemaFor[probeAnswer]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if schema.Type != genai.TypeObject {
		t.Fatalf("root type = %v, want object", schema.Type)
	}
	if got := schema.Properties["keywords"]; got == nil || got.Type != genai.TypeArray || got.Items == nil {
		t.Fatalf("keywords schema = %+v, want array with items", got)
	}
	if got := schema.Properties["decision"]; got == nil || got.Type != genai.TypeString {
		t.Fatalf("decision schema = %+v, want string", got)
	}
	// omitempty fields are optional, everything else is required.
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	if !required["decision"] || required["notes"] {
		t.Fatalf("required = %v, want decision required and notes optional", schema.Required)
	}
}

func decodeRequest(t *testing.T, body []byte) request {
	t.Helper()
	var decoded request
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return decoded
}

func TestBuildRequestForcesTheAnswerWhenNoToolsExist(t *testing.T) {
	schema, err := SchemaFor[probeAnswer]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	m := New("probe-model", "key", "https://example.invalid/v1")
	body, err := m.buildRequest(&model.LLMRequest{
		Model:    "probe-model",
		Config:   &genai.GenerateContentConfig{ResponseSchema: schema, ResponseMIMEType: "application/json"},
		Contents: []*genai.Content{genai.NewContentFromText("pick one", genai.RoleUser)},
	}, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	decoded := decodeRequest(t, body)
	if len(decoded.Tools) != 1 || decoded.Tools[0].Function.Name != structuredOutputTool {
		t.Fatalf("tools = %+v, want only the answer function", decoded.Tools)
	}
	if decoded.Tools[0].Function.Parameters["properties"] == nil {
		t.Fatalf("answer function carries no schema properties: %+v", decoded.Tools[0].Function.Parameters)
	}
	forced, ok := decoded.ToolChoice["function"].(map[string]any)
	if !ok || forced["name"] != structuredOutputTool {
		t.Fatalf("tool_choice = %+v, want the answer function forced", decoded.ToolChoice)
	}
}

func TestBuildRequestSendsTheAgentInstruction(t *testing.T) {
	m := New("probe-model", "key", "https://example.invalid/v1")
	body, err := m.buildRequest(&model.LLMRequest{
		Model: "probe-model",
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("You extract purchase intent.", genai.RoleUser),
		},
		Contents: []*genai.Content{genai.NewContentFromText("buy me a trimmer", genai.RoleUser)},
	}, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	decoded := decodeRequest(t, body)
	if len(decoded.Messages) != 2 {
		t.Fatalf("messages = %+v, want instruction plus user turn", decoded.Messages)
	}
	if decoded.Messages[0].Role != "system" || decoded.Messages[0].Content != "You extract purchase intent." {
		t.Fatalf("first message = %+v, want the instruction as a system turn", decoded.Messages[0])
	}
	if decoded.Messages[1].Role != "user" {
		t.Fatalf("second message = %+v, want the user turn", decoded.Messages[1])
	}
}

func TestBuildRequestLeavesRealToolsCallable(t *testing.T) {
	schema, err := SchemaFor[probeAnswer]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	m := New("probe-model", "key", "https://example.invalid/v1")
	body, err := m.buildRequest(&model.LLMRequest{
		Model: "probe-model",
		Config: &genai.GenerateContentConfig{
			ResponseSchema: schema,
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
				{Name: "counter_offer", Description: "propose an amount"},
			}}},
		},
		Contents: []*genai.Content{genai.NewContentFromText("negotiate", genai.RoleUser)},
	}, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	decoded := decodeRequest(t, body)
	if len(decoded.Tools) != 2 {
		t.Fatalf("tools = %+v, want the real tool plus the answer function", decoded.Tools)
	}
	// Forcing the answer here would make the real tool unreachable.
	if decoded.ToolChoice != nil {
		t.Fatalf("tool_choice = %+v, want unset so tools stay callable", decoded.ToolChoice)
	}
}

func TestAdaptTurnsTheAnswerCallIntoText(t *testing.T) {
	m := New("probe-model", "key", "https://example.invalid/v1")
	decoded := &response{}
	decoded.Choices = append(decoded.Choices, struct {
		Message struct {
			Content   *string    `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{})
	decoded.Choices[0].Message.ToolCalls = []toolCall{{
		ID: "call_1", Type: "function",
		Function: function{Name: structuredOutputTool, Arguments: `{"decision":"negotiate","reason":"too dear"}`},
	}}

	out := m.adapt(decoded, true)
	if out.ErrorMessage != "" {
		t.Fatalf("adapt error: %s", out.ErrorMessage)
	}
	if len(out.Content.Parts) != 1 || out.Content.Parts[0].Text == "" {
		t.Fatalf("parts = %+v, want a single text part", out.Content.Parts)
	}
	var answer probeAnswer
	if err := json.Unmarshal([]byte(out.Content.Parts[0].Text), &answer); err != nil {
		t.Fatalf("answer is not JSON: %v", err)
	}
	if answer.Decision != "negotiate" {
		t.Fatalf("decision = %q, want negotiate", answer.Decision)
	}

	// A real tool call still arrives as a call, not as text.
	decoded.Choices[0].Message.ToolCalls[0].Function.Name = "counter_offer"
	out = m.adapt(decoded, true)
	if len(out.Content.Parts) != 1 || out.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("parts = %+v, want a function call", out.Content.Parts)
	}
}

func TestAnAnswerWithoutTheShapeIsAskedAgainForced(t *testing.T) {
	// The first reply is prose, which a stage expecting a shape cannot use. The
	// second must arrive through the answer function.
	var forced []bool
	replies := []string{
		`{"choices":[{"message":{"role":"assistant","content":"I would push back on that price."}}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"final_answer","arguments":"{\"decision\":\"counter\",\"reason\":\"too high\"}"}}]}}]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forced = append(forced, strings.Contains(string(body), `"tool_choice"`))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(replies[min(len(forced)-1, len(replies)-1)]))
	}))
	defer server.Close()

	shape, err := SchemaFor[probeAnswer]()
	if err != nil {
		t.Fatal(err)
	}
	request := &model.LLMRequest{
		Model: "stand-in",
		Config: &genai.GenerateContentConfig{
			ResponseSchema: shape,
			// A real tool alongside the answer is what stops the answer being
			// forced on the first ask.
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: "counter", Description: "push back", Parameters: shape,
			}}}},
		},
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "they quoted high"}}}},
	}

	var answer string
	for response, err := range New("stand-in", "key", server.URL).GenerateContent(t.Context(), request, false) {
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		for _, part := range response.Content.Parts {
			answer += part.Text
		}
	}

	if len(forced) != 2 {
		t.Fatalf("made %d calls, want a second with the shape forced", len(forced))
	}
	if forced[0] {
		t.Fatal("the first ask must leave the real tool callable")
	}
	if !forced[1] {
		t.Fatal("the second ask must force the answer")
	}
	shaped := map[string]any{}
	if err := json.Unmarshal([]byte(answer), &shaped); err != nil {
		t.Fatalf("answer %q is not the shape that was asked for: %v", answer, err)
	}
	if shaped["decision"] != "counter" {
		t.Fatalf("decision = %v", shaped["decision"])
	}
}

func TestASpentModelLeavesTheChainInsteadOfBeingAskedAgain(t *testing.T) {
	asked := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		asked[sent.Model]++
		if sent.Model == "spent-model" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"FreeUsageLimitError","message":"free usage limit reached"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	m := New("spent-model,working-model", "key", server.URL)
	for call := 1; call <= 3; call++ {
		if _, err := m.post(context.Background(), []byte(`{"model":"spent-model","messages":[]}`)); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}

	if asked["spent-model"] != 1 {
		t.Fatalf("asked the spent model %d times, want 1: a spent allowance must not be retried or re-probed", asked["spent-model"])
	}
	if asked["working-model"] != 3 {
		t.Fatalf("asked the working model %d times, want 3", asked["working-model"])
	}
}

func TestAFlappingModelIsAskedAgainRatherThanAbandoned(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}`))
	}))
	defer server.Close()

	m := New("flapping-model", "key", server.URL)
	if _, err := m.post(context.Background(), []byte(`{"model":"flapping-model","messages":[]}`)); err == nil {
		t.Fatal("a model that never answers should report failure")
	}
	if calls != 5 {
		t.Fatalf("asked %d times, want 5: a pool flap is decided per call, so the budget must be spent before giving up", calls)
	}
}

func TestAnAnswerCutOffIsAskedAgainInsteadOfActedOn(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if sent.Model != "one-model" {
			t.Fatalf("switched to %q instead of asking the same model again", sent.Model)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"the trimmer is worth it because"},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"the trimmer is worth it because it holds its charge."},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	m := New("one-model", "key", server.URL)
	decoded, err := m.post(context.Background(), []byte(`{"model":"one-model","messages":[]}`))
	if err != nil {
		t.Fatalf("the second answer was complete and should have been returned: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if decoded.Choices[0].FinishReason != "stop" {
		t.Fatalf("kept the cut answer: finish reason %q", decoded.Choices[0].FinishReason)
	}
}
