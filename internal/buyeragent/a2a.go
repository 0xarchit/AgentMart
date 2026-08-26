// Package buyeragent publishes AgentMart's buyer as a discoverable agent.
//
// Scope is deliberately quote-only: an external agent can ask ours to shop,
// intent, catalog search, selection, and a full merchant negotiation, and gets
// the settled terms plus the conversation transcript back. It cannot move
// money. Wallet debits stay on the Telegram path where the human is identified
// and the Gate plus approval flow apply.
package buyeragent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// Shopper is the buyer graph surface this agent exposes.
type Shopper interface {
	Run(ctx context.Context, request string, wallet shopgraph.Wallet) (shopgraph.Result, error)
}

// NewHandler builds the buyer agent card and JSON-RPC handler. endpoint is the
// publicly reachable JSON-RPC mount advertised in the card.
func NewHandler(shopper Shopper, endpoint string) (http.Handler, error) {
	if shopper == nil {
		return nil, fmt.Errorf("buyer agent needs a shopper")
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("buyer agent endpoint is required")
	}
	handler := a2asrv.NewHandler(&executor{shopper: shopper})
	card := &a2a.AgentCard{
		Name:                "agentmart-buyer",
		Description:         "Shops the AgentMart catalog on a user's behalf: resolves intent, negotiates with the merchant agent, and returns settled terms with the full negotiation transcript. Quote-only: it never moves money.",
		Version:             "v1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: false},
		DefaultInputModes:   []string{"application/json"},
		DefaultOutputModes:  []string{"application/json"},
		Skills: []a2a.AgentSkill{{
			ID:          "negotiate_purchase",
			Name:        "Negotiated shopping quote",
			Description: `Send {"request":"a trimmer under 2500","budget_paise":250000}. Returns the chosen product, negotiated amount, whether the merchant accepted, and the negotiation transcript.`,
			Tags:        []string{"commerce", "negotiation", "shopping", "quote"},
		}},
	}
	mux := http.NewServeMux()
	mux.Handle("/.well-known/agent-card.json", a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(handler))
	return mux, nil
}

type executor struct {
	shopper Shopper
}

// shopRequest is the skill's input contract.
type shopRequest struct {
	Request     string `json:"request"`
	BudgetPaise int64  `json:"budget_paise,omitempty"`
}

// shopResponse is the skill's output contract: terms and evidence, no money.
type shopResponse struct {
	Action           string             `json:"action"`
	ProductID        string             `json:"product_id,omitempty"`
	ProductName      string             `json:"product_name,omitempty"`
	Quantity         int                `json:"quantity,omitempty"`
	FinalAmountPaise int64              `json:"final_amount_paise,omitempty"`
	Rationale        string             `json:"rationale,omitempty"`
	SessionID        string             `json:"session_id,omitempty"`
	MerchantAccepted bool               `json:"merchant_accepted"`
	NeedsHuman       bool               `json:"needs_human_approval"`
	Steps            []string           `json:"steps,omitempty"`
	Transcript       []negotiation.Turn `json:"transcript,omitempty"`
	Note             string             `json:"note"`
}

const quoteOnlyNote = "quote only: AgentMart never debits a wallet for an agent caller; settle through the owning user's approved checkout"

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.Message == nil || len(execCtx.Message.Parts) == 0 {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("a shopping request is required"))), nil)
			return
		}
		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		var request shopRequest
		payload := strings.TrimSpace(execCtx.Message.Parts[0].Text())
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			// Plain text is a valid request too: treat the message as the ask.
			request = shopRequest{Request: payload}
		}
		if strings.TrimSpace(request.Request) == "" {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateRejected,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("request text is empty"))), nil)
			return
		}

		// Budget doubles as the spending ceiling: no account, no wallet, so the
		// graph reasons against the caller's stated limit only.
		budget := request.BudgetPaise
		wallet := shopgraph.Wallet{BalancePaise: budget, SpendLimitPaise: budget, BudgetPaise: budget}

		result, err := e.shopper.Run(ctx, request.Request, wallet)
		if err != nil {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))), nil)
			return
		}

		response := shopResponse{
			Action: string(result.Action), ProductID: result.ProductID, ProductName: result.ProductName,
			Quantity: result.Quantity, FinalAmountPaise: result.FinalPaise, Rationale: result.Rationale,
			SessionID: result.SessionID, MerchantAccepted: result.Accepted, NeedsHuman: result.NeedsApproval,
			Steps: result.Steps, Transcript: result.Transcript, Note: quoteOnlyNote,
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("encode shopping response failed"))), nil)
			return
		}
		if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(string(encoded))), nil) {
			return
		}
		// A quote that needs the owner's sign-off is reported as input-required
		// so the calling agent knows a human still has to approve the spend.
		state := a2a.TaskStateCompleted
		if result.NeedsApproval {
			state = a2a.TaskStateInputRequired
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, state, nil), nil)
	}
}

func (e *executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}
