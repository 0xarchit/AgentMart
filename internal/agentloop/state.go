// Per-run mutable state shared by the supervisor, tools, and both engines.
package agentloop

import (
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
)

// runState is one run's scratchpad. The buyer bot processes updates serially,
// so a single active state on the Service is safe; guard it again if the bot
// ever fans out per-user goroutines.
type runState struct {
	wallet      WalletFacts
	request     string
	productID   string
	quantity    int
	finalPaise  int64
	sessionID   string
	rationale   string
	steps       []string
	transcript  []negotiation.Turn
	toolCalls   int
	counterUsed int
	accepted    bool

	offers map[string]negotiationclient.Proposal // session id → observed quote
	finish *finishDecision
	human  *humanRequest
}

type finishDecision struct {
	Action     string `json:"action"`
	SessionID  string `json:"session_id"`
	ProductID  string `json:"product_id"`
	Quantity   int    `json:"quantity"`
	FinalPaise int64  `json:"final_paise"`
	Rationale  string `json:"rationale"`
}

type humanRequest struct {
	Reason      string
	AmountPaise int64
	SessionID   string
}

func newState(wallet WalletFacts, request string) *runState {
	return &runState{wallet: wallet, request: request, quantity: 1, offers: map[string]negotiationclient.Proposal{}}
}

func (s *runState) step(message string) {
	s.steps = append(s.steps, message)
}

func (s *runState) doneTool() {}

// mergeTranscript adopts the server's full-session snapshot when present;
// empty snapshots never wipe turns already collected.
func mergeTranscript(current, incoming []negotiation.Turn) []negotiation.Turn {
	if len(incoming) > 0 {
		return incoming
	}
	return current
}
