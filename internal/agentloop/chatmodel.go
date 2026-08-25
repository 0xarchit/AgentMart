// Chat-completions adapter implementing adk model.LLM.
//
// Why this exists: ADK v2.2.0's openaimodel hardcodes OpenAI's Responses API
// (POST {base}/responses), which OpenRouter free models, NVIDIA NIM, and most
// OpenAI-compatible gateways do not implement. Every such provider DOES speak
// POST {base}/chat/completions with tool calling. This adapter speaks that
// wire format so any provider works.
package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ChatModel is a minimal chat-completions model.LLM with function calling.
// Non-streaming by design: one complete response per GenerateContent call.
type ChatModel struct {
	name    string
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewChatModel builds a chat-completions-backed LLM.
func NewChatModel(name, apiKey, baseURL string) *ChatModel {
	return &ChatModel{name: name, apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{}}
}

func (m *ChatModel) Name() string { return m.name }

// ccRequest/ccMessage/ccTool mirror the wire format of /chat/completions.
type ccRequest struct {
	Model     string       `json:"model"`
	Messages  []ccMessage  `json:"messages"`
	Tools     []ccTool     `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

type ccMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string | nil for assistant tool_call turns
	ToolCalls  []ccToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ccToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function ccFnCall `json:"function"`
}

type ccFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ccTool struct {
	Type     string           `json:"type"`
	Function ccFunctionSchema `json:"function"`
}

type ccFunctionSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ccResponse struct {
	Choices []struct {
		Message struct {
			Content   *string     `json:"content"`
			ToolCalls []ccToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// GenerateContent implements adk model.LLM over /chat/completions.
func (m *ChatModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if stream {
			// The runner tolerates a single final event; streaming adds nothing here.
			yield(nil, fmt.Errorf("chat model does not support streaming"))
			return
		}
		body, err := m.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			yield(nil, fmt.Errorf("build chat request: %w", err))
			return
		}
		httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := m.http.Do(httpReq)
		if err != nil {
			yield(nil, fmt.Errorf("chat completions call failed: %w", err))
			return
		}
		defer resp.Body.Close()
		var decoded ccResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			yield(nil, fmt.Errorf("decode chat completions response (%s): %w", resp.Status, err))
			return
		}
		if decoded.Error != nil {
			yield(&model.LLMResponse{ErrorCode: fmt.Sprintf("%d", resp.StatusCode), ErrorMessage: decoded.Error.Message}, nil)
			return
		}
		if len(decoded.Choices) == 0 {
			yield(nil, fmt.Errorf("chat completions returned no choices"))
			return
		}
		choice := decoded.Choices[0]
		response := &model.LLMResponse{
			Content:      &genai.Content{Role: "model"},
			TurnComplete: true,
		}
		if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" {
			response.Content.Parts = append(response.Content.Parts, &genai.Part{Text: *choice.Message.Content})
		}
		for i, call := range choice.Message.ToolCalls {
			args := map[string]any{}
			if strings.TrimSpace(call.Function.Arguments) != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					yield(nil, fmt.Errorf("decode tool arguments for %s: %w", call.Function.Name, err))
					return
				}
			}
			id := call.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			response.Content.Parts = append(response.Content.Parts,
				&genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: call.Function.Name, Args: args}})
		}
		yield(response, nil)
	}
}

// buildRequest converts the ADK conversation into chat-completions messages.
func (m *ChatModel) buildRequest(req *model.LLMRequest) ([]byte, error) {
	out := ccRequest{Model: req.Model}
	if req.Config != nil {
		for _, tool := range req.Config.Tools {
			for _, decl := range tool.FunctionDeclarations {
				out.Tools = append(out.Tools, ccTool{Type: "function", Function: ccFunctionSchema{
					Name: decl.Name, Description: decl.Description,
					Parameters: schemaAsMap(decl.Parameters),
				}})
			}
		}
	}
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		switch content.Role {
		case "model":
			msg := ccMessage{Role: "assistant"}
			var textParts []string
			for _, part := range content.Parts {
				switch {
				case part.Text != "":
					textParts = append(textParts, part.Text)
				case part.FunctionCall != nil:
					argsJSON := "{}"
					if len(part.FunctionCall.Args) > 0 {
						encoded, err := json.Marshal(part.FunctionCall.Args)
						if err != nil {
							return nil, fmt.Errorf("encode function call args: %w", err)
						}
						argsJSON = string(encoded)
					}
					msg.ToolCalls = append(msg.ToolCalls, ccToolCall{
						ID: toolCallID(part.FunctionCall.ID), Type: "function",
						Function: ccFnCall{Name: part.FunctionCall.Name, Arguments: argsJSON},
					})
				}
			}
			if len(textParts) > 0 {
				msg.Content = strings.Join(textParts, "\n")
			}
			out.Messages = append(out.Messages, msg)
		case "tool":
			for _, part := range content.Parts {
				if part.FunctionResponse == nil {
					continue
				}
				payload := part.FunctionResponse.Response
				if payload == nil {
					payload = map[string]any{}
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					return nil, fmt.Errorf("encode function response: %w", err)
				}
				out.Messages = append(out.Messages, ccMessage{
					Role: "tool", ToolCallID: toolCallID(part.FunctionResponse.Name),
					Content: string(encoded),
				})
			}
		default: // user and anything else
			var textParts []string
			for _, part := range content.Parts {
				if part.Text != "" {
					textParts = append(textParts, part.Text)
				}
			}
			out.Messages = append(out.Messages, ccMessage{Role: "user", Content: strings.Join(textParts, "\n")})
		}
	}
	return json.Marshal(out)
}

// toolCallID passes provider IDs through; empty IDs are matched downstream by
// function name order.
func toolCallID(id string) string { return id }

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
