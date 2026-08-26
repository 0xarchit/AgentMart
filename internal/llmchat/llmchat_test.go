// Tests for the structured-answer path: a caller that asks for a result shape
// must get JSON in that shape, not prose.
package llmchat

import (
	"encoding/json"
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
	})
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
	})
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
	})
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
