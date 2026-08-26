// Package merchantagent exposes the merchant negotiation agent to other agents.
package merchantagent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// NewHandler constructs the merchant agent card and JSON-RPC handler.
func NewHandler(getProduct func(context.Context, string) (catalog.Product, error), store negotiation.SessionStore, endpoint string) (http.Handler, error) {
	server, err := negotiation.NewCatalogServerWithStore(getProduct, store)
	if err != nil {
		return nil, err
	}
	executor := &executor{server: server}
	handler := a2asrv.NewHandler(executor)
	card := &a2a.AgentCard{
		Name:                "merchant-negotiation",
		Description:         "Negotiates catalog offers and returns accepted purchase amounts.",
		Version:             "v1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: false},
		DefaultInputModes:   []string{"application/json"},
		DefaultOutputModes:  []string{"application/json"},
		Skills:              []a2a.AgentSkill{{ID: "negotiation", Name: "Catalog negotiation", Description: "Propose, accept, or decline a merchant counter offer.", Tags: []string{"catalog", "negotiation", "commerce"}}},
	}
	mux := http.NewServeMux()
	mux.Handle("/.well-known/agent-card.json", a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(handler))
	return mux, nil
}

type executor struct {
	server *negotiation.Server
}

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.Message == nil || len(execCtx.Message.Parts) == 0 {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("negotiation message is required"))), nil)
			return
		}
		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		payload := strings.TrimSpace(execCtx.Message.Parts[0].Text())
		var request struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id,omitempty"`
			ProductID string `json:"product_id,omitempty"`
			Quantity  int    `json:"qty,omitempty"`
			Reason    string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("invalid negotiation payload"))), nil)
			return
		}
		response, err := e.handle(ctx, request)
		if err != nil {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))), nil)
			return
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("encode negotiation response failed"))), nil)
			return
		}
		if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(string(encoded))), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

func (e *executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func (e *executor) handle(ctx context.Context, request struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Quantity  int    `json:"qty,omitempty"`
	Reason    string `json:"reason,omitempty"`
}) (any, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	requestReader := strings.NewReader(string(body))
	requestHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://merchant.internal/negotiation", requestReader)
	if err != nil {
		return nil, err
	}
	requestHTTP.Header.Set("Content-Type", "application/json")
	responseRecorder := &captureResponse{header: make(http.Header)}
	e.server.Handler().ServeHTTP(responseRecorder, requestHTTP)
	if responseRecorder.status >= http.StatusMultipleChoices || responseRecorder.status == 0 {
		return nil, fmt.Errorf("merchant negotiation returned status %d", responseRecorder.status)
	}
	var response any
	if err := json.Unmarshal(responseRecorder.body, &response); err != nil {
		return nil, err
	}
	return response, nil
}

type captureResponse struct {
	header http.Header
	body   []byte
	status int
}

func (r *captureResponse) Header() http.Header    { return r.header }
func (r *captureResponse) WriteHeader(status int) { r.status = status }
func (r *captureResponse) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, body...)
	return len(body), nil
}
