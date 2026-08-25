// The LLM path: typed function tools wired into one llmagent run.
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"agentmart/internal/catalog"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

const maxCountersOnce = 1

// active holds the in-flight run state for tool handlers. ADK handlers receive
// agent.Context, not our state; the buyer bot is serial so one guarded slot is
// safe. Revisit if the bot ever fans out per-user goroutines.
var (
	activeMu sync.Mutex
	active   *runState
)

func setActive(st *runState) { activeMu.Lock(); active = st; activeMu.Unlock() }

func clearActive() { activeMu.Lock(); active = nil; activeMu.Unlock() }

func beginTool() *runState {
	activeMu.Lock()
	defer activeMu.Unlock()
	if active == nil {
		active = newState(WalletFacts{}, "")
	}
	state := active
	state.toolCalls++
	return state
}

// modelTools builds the agent-facing tools once per Service. Handlers read the
// active run state; closures over s are unnecessary because tools only touch
// state plus the injected Tools funcs captured at New time via s.tools.
func (s *Service) modelTools() []tool.Tool {
	search := mustTool("search_catalog", "Find merchant products by free-text query with an optional maximum total price in paise.",
		func(ctx agent.Context, in searchInput) ([]catalog.Product, error) {
			st := beginTool()
			defer st.doneTool()
			maxPaise := st.wallet.SpendLimitPaise
			if st.wallet.BudgetPaise > 0 {
				maxPaise = st.wallet.BudgetPaise
			}
			return s.tools.Search(ctx, strings.TrimSpace(in.Query), maxPaise)
		})
	get := mustTool("get_product", "Read one product's full facts.",
		func(ctx agent.Context, in productInput) (catalog.Product, error) {
			st := beginTool()
			defer st.doneTool()
			st.productID = in.ProductID
			return s.tools.Get(ctx, in.ProductID)
		})
	offers := mustTool("get_offers", "Get the merchant quote for a product and quantity, including any combo bundle.",
		func(ctx agent.Context, in offersInput) (offersOutput, error) {
			st := beginTool()
			defer st.doneTool()
			proposal, err := s.tools.Offers(ctx, in.ProductID, in.Quantity)
			if err != nil {
				return offersOutput{}, err
			}
			st.offers[proposal.SessionID] = proposal
			st.transcript = mergeTranscript(st.transcript, proposal.Transcript)
			st.productID = proposal.ProductID
			st.quantity = proposal.Quantity
			st.sessionID = proposal.SessionID
			if st.finalPaise == 0 {
				st.finalPaise = proposal.FinalAmountPaise
			}
			st.step(fmt.Sprintf("offer %s: INR %.2f (%s)", short(proposal.SessionID), float64(proposal.FinalAmountPaise)/100, proposal.Reason))
			return offersOutput{SessionID: proposal.SessionID, ProductID: proposal.ProductID, Quantity: proposal.Quantity,
				BasePaise: proposal.BaseAmountPaise, FinalPaise: proposal.FinalAmountPaise, Reason: proposal.Reason,
				Name: proposal.Name, Category: proposal.Category, Stock: proposal.Stock}, nil
		})
	counter := mustTool("counter_offer", "Submit one counter amount against an open session.",
		func(ctx agent.Context, in counterInput) (counterOutput, error) {
			st := beginTool()
			defer st.doneTool()
			if st.counterUsed >= maxCountersOnce {
				return counterOutput{}, fmt.Errorf("only one counter per negotiation")
			}
			resolution, err := s.tools.Counter(ctx, in.SessionID, in.AmountPaise)
			if err != nil {
				return counterOutput{}, err
			}
			st.counterUsed++
			st.sessionID = resolution.SessionID
			st.finalPaise = resolution.FinalAmountPaise
			if resolution.Status == "accepted" {
				st.accepted = true
			}
			st.transcript = append(st.transcript, resolution.Transcript...)
			st.step(fmt.Sprintf("counter -> %s at INR %.2f", resolution.Status, float64(resolution.FinalAmountPaise)/100))
			return counterOutput{Status: string(resolution.Status), FinalPaise: resolution.FinalAmountPaise}, nil
		})
	accept := mustTool("accept_offer", "Formally accept the merchant's current offer for an open session.",
		func(ctx agent.Context, in acceptInput) (counterOutput, error) {
			st := beginTool()
			defer st.doneTool()
			resolution, err := s.tools.Accept(ctx, in.SessionID)
			if err != nil {
				return counterOutput{}, err
			}
			st.accepted = true
			st.sessionID = resolution.SessionID
			if resolution.FinalAmountPaise > 0 {
				st.finalPaise = resolution.FinalAmountPaise
			}
			st.transcript = mergeTranscript(st.transcript, resolution.Transcript)
			st.step(fmt.Sprintf("accepted at INR %.2f", float64(resolution.FinalAmountPaise)/100))
			return counterOutput{Status: string(resolution.Status), FinalPaise: resolution.FinalAmountPaise}, nil
		})
	human := mustTool("request_human", "Ask the human to confirm before buying when the premium crosses the band.",
		func(ctx agent.Context, in humanInput) (string, error) {
			st := beginTool()
			defer st.doneTool()
			st.human = &humanRequest{Reason: in.Reason, AmountPaise: st.finalPaise, SessionID: st.sessionID}
			st.step(fmt.Sprintf("ask_human: %s", in.Reason))
			return "the human will be asked before any purchase", nil
		})
	finish := mustTool("finish", "Record the final decision. Call exactly once at the end.",
		func(ctx agent.Context, in finishDecision) (string, error) {
			st := beginTool()
			defer st.doneTool()
			in.Action = strings.ToLower(strings.TrimSpace(in.Action))
			st.finish = &in
			if in.ProductID != "" {
				st.productID = in.ProductID
			}
			if in.Quantity > 0 {
				st.quantity = in.Quantity
			}
			if in.FinalPaise > 0 {
				st.finalPaise = in.FinalPaise
			}
			if in.SessionID != "" {
				st.sessionID = in.SessionID
			}
			st.rationale = in.Rationale
			st.step(fmt.Sprintf("finish action=%s", in.Action))
			return "decision recorded", nil
		})
	return []tool.Tool{search, get, offers, counter, accept, human, finish}
}

type searchInput struct {
	Query    string `json:"query" jsonschema:"free-text product or category keywords"`
	MaxPaise int64  `json:"max_paise,omitempty" jsonschema:"optional maximum price filter in paise"`
}

type productInput struct {
	ProductID string `json:"product_id" jsonschema:"the catalog product identifier"`
}

type offersInput struct {
	ProductID string `json:"product_id" jsonschema:"the catalog product identifier"`
	Quantity  int    `json:"quantity" jsonschema:"how many units"`
}

type offersOutput struct {
	SessionID  string `json:"session_id"`
	ProductID  string `json:"product_id"`
	Quantity   int    `json:"quantity"`
	BasePaise  int64  `json:"base_amount_paise"`
	FinalPaise int64  `json:"final_amount_paise"`
	Reason     string `json:"reason"`
	Name       string `json:"name,omitempty"`
	Category   string `json:"category,omitempty"`
	Stock      int    `json:"stock,omitempty"`
}

type counterInput struct {
	SessionID   string `json:"session_id" jsonschema:"session returned by get_offers"`
	AmountPaise int64  `json:"amount_paise" jsonschema:"your counter amount in paise"`
}

type counterOutput struct {
	Status     string `json:"status"`
	FinalPaise int64  `json:"final_amount_paise"`
}

type acceptInput struct {
	SessionID string `json:"session_id" jsonschema:"session returned by get_offers"`
}

type humanInput struct {
	Reason string `json:"reason" jsonschema:"why the human should decide"`
}

func mustTool[TArgs, TResult any](name, description string, fn func(agent.Context, TArgs) (TResult, error)) tool.Tool {
	wrapped, err := functiontool.New(functiontool.Config{Name: name, Description: description},
		functiontool.Func[TArgs, TResult](fn))
	if err != nil {
		panic(fmt.Sprintf("agentloop: build tool %s: %v", name, err))
	}
	return wrapped
}

// llmRun executes one bounded model loop over the shared runner.
func (s *Service) llmRun(ctx context.Context, state *runState) error {
	setActive(state)
	defer clearActive()

	facts := map[string]any{
		"request":              state.request,
		"wallet_balance_paise": state.wallet.BalancePaise,
		"spend_limit_paise":    state.wallet.SpendLimitPaise,
		"budget_paise":         state.wallet.BudgetPaise,
		"premium_band_pct":     AutoBuyPremiumMaxPct,
	}
	payload, err := json.Marshal(facts)
	if err != nil {
		return fmt.Errorf("encode loop facts: %w", err)
	}
	sessionID := fmt.Sprintf("buyer-loop-%d", s.sessions.Add(1))
	events := 0
	for event, runErr := range s.runner.Run(ctx, "buyer", sessionID, genai.NewContentFromText(string(payload), genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			return fmt.Errorf("runner error after %d events: %w", events, runErr)
		}
		events++
		if events > maxModelEvents {
			return fmt.Errorf("model exceeded %d events without finishing", maxModelEvents)
		}
		if event != nil && event.Content != nil {
			for _, part := range event.Content.Parts {
				if part != nil && part.Text != "" && strings.TrimSpace(part.Text) != "" {
					state.rationale = strings.TrimSpace(part.Text)
				}
			}
		}
	}
	if state.finish == nil && state.human == nil {
		return fmt.Errorf("loop ended without a decision")
	}
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
