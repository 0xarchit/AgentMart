// Strict-JSON single-function-call completion over any OpenAI-compatible
// provider. Forces the model to emit exactly one structured payload, the
// building block for every pipeline stage.
package llmchat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Completer is the minimal surface pipeline stages depend on. *Model implements
// it; tests provide scripted stubs.
type Completer interface {
	CompleteJSON(ctx context.Context, req CompleteRequest) (map[string]any, error)
}

// CompleteRequest describes one forced-function-call completion.
type CompleteRequest struct {
	System       string         // role/system instruction for the stage
	User         string         // JSON-encoded stage facts
	FunctionName string         // the single callable function
	Description  string         // what completing it means
	Parameters   map[string]any // JSON-schema parameters object
	Temperature  float32
}

// CompleteJSON forces one function call and returns its decoded arguments.
func (m *Model) CompleteJSON(ctx context.Context, req CompleteRequest) (map[string]any, error) {
	out := request{
		Model: m.name,
		Messages: []message{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Tools: []tool{{
			Type: "function",
			Function: toolFunction{
				Name: req.FunctionName, Description: req.Description, Parameters: req.Parameters,
			},
		}},
	}
	fnName := req.FunctionName
	out.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": fnName}}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("llmchat: encode completion: %w", err)
	}
	decoded, err := m.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("llmchat: no choices")
	}
	for _, call := range decoded.Choices[0].Message.ToolCalls {
		if call.Function.Name != fnName || strings.TrimSpace(call.Function.Arguments) == "" {
			continue
		}
		args := map[string]any{}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("llmchat: decode %s args: %w", fnName, err)
		}
		return args, nil
	}
	// Some gateways answer content-only even under forced tool choice; parse it.
	content := ""
	if len(decoded.Choices) > 0 && decoded.Choices[0].Message.Content != nil {
		content = strings.TrimSpace(*decoded.Choices[0].Message.Content)
	}
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	if content != "" {
		args := map[string]any{}
		if err := json.Unmarshal([]byte(content), &args); err == nil {
			return args, nil
		}
	}
	return nil, fmt.Errorf("llmchat: %s was not called and no JSON content returned", fnName)
}
