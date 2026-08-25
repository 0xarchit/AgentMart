// Graph construction and runtime for AgentMart's buying agent.
//
// Layout (ADK Go 2.0 workflow engine):
//
//	START → intent(LLM) → search(MCP) → select(LLM) → offer(A2A propose + band route)
//	    offer ──ACCEPT───▶ accept(A2A) ─────────────┐
//	    offer ──NEGOTIATE▶ negotiate(LLM⇄A2A loop) ─┴─▶ finalize(verify) → END
//	    offer ──DECLINE──▶ declined(outcome) ────────▶ END
//
// Every judgment node is an LLM agent; routing/floors/caps are deterministic.
package shopgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/llmchat"
	"agentmart/internal/negotiationclient"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// AutoBuyPremiumMaxPct is guidance surfaced to the negotiating agent: offers
// above this premium over list are escalated to the human instead of bought.
const AutoBuyPremiumMaxPct = 30

// Config selects the provider backing every LLM node.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// Service owns the compiled graph and per-run wallet slot. The buyer bot is
// serial, so one slot is safe; revisit if runs ever fan out per user.
type Service struct {
	tools   Tools
	model   *llmchat.Model
	mu      sync.Mutex
	wallet  Wallet
	wfAgent agent.Agent
}

// New compiles the graph. Safe to call without network: models are only used
// when a run executes.
func New(ctx context.Context, cfg Config, tools Tools) (*Service, error) {
	if err := tools.validate(); err != nil {
		return nil, err
	}
	s := &Service{tools: tools, model: llmchat.New(cfg.Model, cfg.APIKey, cfg.BaseURL)}
	root, err := s.buildGraph()
	if err != nil {
		return nil, err
	}
	s.wfAgent = root
	return s, nil
}

func (s *Service) walletSnapshot() Wallet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wallet
}

func (s *Service) setWallet(w Wallet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallet = w
}

func (s *Service) decisionModel() model.LLM { return s.model }

var (
	intentInstruction = `Extract purchase intent from the user's message.
Return keywords that name the product or category (e.g. ["trimmer"]), and
budget_paise parsed from phrases like "under 2500" (rupees -> x100). Use 0 when
no budget was stated.`

	selectInstruction = `Choose exactly one product from the candidates for the
user's request. Prefer relevance first, then lower price, then higher
trust_score. Return the chosen product_id, quantity, and one-line rationale. If
nothing matches, return product_id "" with the reason.`

	negotiateInstruction = fmt.Sprintf(`You are AgentMart's negotiating buyer.
You receive the merchant's opening offer (session_id, final_amount_paise,
base_amount_paise) plus wallet facts and premium_band_pct=%d. Decide:
- If the offer fits your constraints, call accept_offer(session_id).
- Otherwise call counter_offer(session_id, amount_paise) ONCE at about 85%% of
  the ask. Then judge the response: accept_offer if it now fits, decline_offer
  otherwise. Never exceed wallet_balance_paise or budget_paise.
Finish with a short summary of what you did and why.`, AutoBuyPremiumMaxPct)
)

// Per-node time bounds.
const (
	nodeTimeout      = 90 * time.Second
	negotiateTimeout = 180 * time.Second
)

// Route names emitted by fetch_offer.
const (
	RouteAccept    = "ACCEPT"
	RouteNegotiate = "NEGOTIATE"
	RouteDecline   = "DECLINE"
)

// buildGraph wires every node and wraps the workflow as a standard agent.
func (s *Service) buildGraph() (agent.Agent, error) {
	intentAgent, err := llmagent.New(llmagent.Config{
		Name: "intent-agent", Model: s.decisionModel(), Instruction: intentInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("intent agent: %w", err)
	}
	selectAgent, err := llmagent.New(llmagent.Config{
		Name: "select-agent", Model: s.decisionModel(), Instruction: selectInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("select agent: %w", err)
	}
	negotiateAgent, err := llmagent.New(llmagent.Config{
		Name:        "negotiate-agent",
		Model:       s.decisionModel(),
		Instruction: negotiateInstruction,
		Tools:       s.negotiationTools(),
	})
	if err != nil {
		return nil, fmt.Errorf("negotiate agent: %w", err)
	}

	intentNode, err := workflow.NewAgentNodeTyped[string, Intent](intentAgent, workflow.NodeConfig{Timeout: nodeTimeout})
	if err != nil {
		return nil, fmt.Errorf("intent node: %w", err)
	}
	searchNode := workflow.NewFunctionNode[Intent, []catalog.Product]("search_catalog",
		func(ctx agent.Context, in Intent) ([]catalog.Product, error) {
			w := s.walletSnapshot()
			maxPaise := w.SpendLimitPaise
			if in.BudgetPaise > 0 {
				maxPaise = in.BudgetPaise
			}
			return s.tools.Search(ctx, strings.Join(in.Keywords, " "), maxPaise)
		}, workflow.NodeConfig{Timeout: nodeTimeout})
	selectNode, err := workflow.NewAgentNodeTyped[[]catalog.Product, Selection](selectAgent, workflow.NodeConfig{Timeout: nodeTimeout})
	if err != nil {
		return nil, fmt.Errorf("select node: %w", err)
	}

	offerNode := workflow.NewEmittingFunctionNode[Selection, Offer]("fetch_offer",
		func(ctx agent.Context, sel Selection, emit func(*session.Event) error) (Offer, error) {
			proposal, err := s.tools.Offers(ctx, sel.ProductID, sel.Quantity)
			if err != nil {
				return Offer{}, err
			}
			offer := Offer{
				SessionID: proposal.SessionID, ProductID: proposal.ProductID,
				ProductName: proposal.Name, Quantity: proposal.Quantity,
				BasePaise: proposal.BaseAmountPaise, FinalPaise: proposal.FinalAmountPaise,
				Reason: proposal.Reason, Transcript: proposal.Transcript,
			}
			offer.Route = classifyOffer(offer, s.walletSnapshot())
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{offer.Route}
			ev.Output = offer
			if err := emit(ev); err != nil {
				return Offer{}, err
			}
			return Offer{}, nil // routed edge carries ev.Output forward
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	acceptNode := workflow.NewFunctionNode[Offer, Outcome]("accept_offer",
		func(ctx agent.Context, offer Offer) (Outcome, error) {
			resolution, err := s.tools.Accept(ctx, offer.SessionID)
			if err != nil {
				return Outcome{}, err
			}
			out := outcomeFrom(offer, resolution)
			out.Action = string(ActionBuy)
			out.Accepted = true
			out.Rationale = "accepted within budget and premium band"
			return out, nil
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	negotiateNode, err := workflow.NewAgentNodeTyped[Offer, Outcome](negotiateAgent, workflow.NodeConfig{Timeout: negotiateTimeout})
	if err != nil {
		return nil, fmt.Errorf("negotiate node: %w", err)
	}

	declinedNode := workflow.NewFunctionNode[Offer, Outcome]("declined_outcome",
		func(ctx agent.Context, offer Offer) (Outcome, error) {
			resolution, derr := s.tools.Decline(ctx, offer.SessionID, "outside my budget")
			out := outcomeFrom(offer, resolution)
			if derr != nil {
				out.Rationale = fmt.Sprintf("decline failed: %v", derr)
			} else {
				out.Rationale = "outside budget or wallet balance"
			}
			out.Action = string(ActionDecline)
			return out, nil
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	builder := workflow.NewEdgeBuilder()
	builder.
		Add(workflow.Start, intentNode).
		Add(intentNode, searchNode).
		Add(searchNode, selectNode).
		Add(selectNode, offerNode)
	builder.AddRoutes(offerNode, map[string]workflow.Node{
		RouteAccept:    acceptNode,
		RouteNegotiate: negotiateNode,
		RouteDecline:   declinedNode,
	})
	// The engine requires branch convergence via JoinNode.
	join := workflow.NewJoinNode("negotiation_join")
	builder.Add(negotiateNode, join).
		Add(acceptNode, join).
		Add(declinedNode, join)

	finalizeNode := workflow.NewFunctionNode[any, Result]("finalize",
		func(ctx agent.Context, raw any) (Result, error) {
			outcome, ok := outcomeFromAny(raw)
			if !ok {
				return Result{}, fmt.Errorf("finalize received %T instead of an Outcome", raw)
			}
			product, err := s.tools.Get(ctx, outcome.ProductID)
			if err != nil {
				return Result{}, fmt.Errorf("verify candidate: %w", err)
			}
			qty := outcome.Quantity
			if qty <= 0 {
				qty = 1
			}
			baseMain := product.PricePaise * int64(qty)
			premium := outcome.FinalPaise - baseMain
			needsApproval := baseMain > 0 && premium > 0 && premium*100 > baseMain*AutoBuyPremiumMaxPct
			action := Action(outcome.Action)
			if action == "" {
				action = ActionBuy
			}
			return Result{
				Action: action, ProductID: product.ID, ProductName: product.Name,
				Quantity: qty, FinalPaise: outcome.FinalPaise, Rationale: outcome.Rationale,
				Steps: outcome.Steps, SessionID: outcome.SessionID, Transcript: outcome.Transcript,
				Accepted: outcome.Accepted, NeedsApproval: needsApproval,
			}, nil
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	builder.Add(join, finalizeNode)

	if _, err := workflow.New("shop-graph", builder.Build()); err != nil {
		return nil, fmt.Errorf("compile shop graph: %w", err)
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "shop-agent",
		Description: "Negotiates and buys merchant products inside bounded rails.",
		Edges:       builder.Build(),
	})
}

// Run executes the graph for one natural-language request and returns the
// verified result. Errors surface the real cause — strict mode.
func (s *Service) Run(parent context.Context, request string, wallet Wallet) (Result, error) {
	if s.wfAgent == nil {
		return Result{}, fmt.Errorf("shop graph is not built")
	}
	s.setWallet(wallet)

	runner, err := runnerFor("shop", s.wfAgent)
	if err != nil {
		return Result{}, err
	}
	facts := map[string]any{
		"request":              request,
		"wallet_balance_paise": wallet.BalancePaise,
		"spend_limit_paise":    wallet.SpendLimitPaise,
		"budget_paise":         wallet.BudgetPaise,
		"premium_band_pct":     AutoBuyPremiumMaxPct,
	}
	payload, err := jsonMarshal(facts)
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(parent, graphRunDeadline)
	defer cancel()

	var lastOutput any
	events := 0
	for event, runErr := range runner.Run(runCtx, "user", fmt.Sprintf("run-%d", time.Now().UnixNano()),
		textContent(string(payload)), defaultRunConfig()) {
		if runErr != nil {
			return Result{}, fmt.Errorf("graph run failed after %d events: %w", events, runErr)
		}
		events++
		if events > maxGraphEvents {
			return Result{}, fmt.Errorf("graph exceeded %d events without finishing", maxGraphEvents)
		}
		if event != nil && event.Output != nil {
			lastOutput = event.Output
		}
	}
	outcome, ok := lastOutput.(Outcome)
	if !ok {
		return Result{}, fmt.Errorf("graph finished without an Outcome (last output %T)", lastOutput)
	}

	result := Result{
		Action: Action(outcome.Action), ProductID: outcome.ProductID,
		ProductName: outcome.ProductName, Quantity: outcome.Quantity,
		FinalPaise: outcome.FinalPaise, Rationale: outcome.Rationale,
		Steps: outcome.Steps, SessionID: outcome.SessionID,
		Transcript: outcome.Transcript, Accepted: outcome.Accepted,
	}
	if result.Quantity <= 0 {
		result.Quantity = 1
	}
	if result.Action == "" {
		result.Action = ActionBuy
	}
	return result, nil
}

// outcomeFromAny extracts an Outcome from a JoinNode aggregate (keyed by
// predecessor name) or a direct pass-through.
func outcomeFromAny(raw any) (Outcome, bool) {
	switch v := raw.(type) {
	case Outcome:
		return v, true
	case map[string]any:
		for _, value := range v {
			if encoded, err := json.Marshal(value); err == nil {
				var out Outcome
				if json.Unmarshal(encoded, &out) == nil && (out.Action != "" || out.Status != "") {
					return out, true
				}
			}
		}
	case string:
		var out Outcome
		if json.Unmarshal([]byte(v), &out) == nil && (out.Action != "" || out.Status != "") {
			return out, true
		}
	}
	return Outcome{}, false
}

// outcomeFrom projects an A2A resolution onto the stage contract.
func outcomeFrom(offer Offer, resolution negotiationclient.Resolution) Outcome {
	out := Outcome{
		Status:      resolution.Status,
		ProductID:   offer.ProductID,
		ProductName: offer.ProductName,
		Quantity:    offer.Quantity,
		SessionID:   resolution.SessionID,
		FinalPaise:  resolution.FinalAmountPaise,
		Transcript:  resolution.Transcript,
	}
	if out.FinalPaise == 0 {
		out.FinalPaise = offer.FinalPaise
	}
	if out.SessionID == "" {
		out.SessionID = offer.SessionID
	}
	return out
}

// Per-run caps.
const (
	graphRunDeadline = 240 * time.Second
	maxGraphEvents   = 200
)

// negotiationTools exposes the A2A conversation moves to the negotiate agent.
func (s *Service) negotiationTools() []tool.Tool {
	counter := mustTool("counter_offer", "Submit one counter amount in paise against the session.",
		func(ctx agent.Context, in counterInput) (counterResult, error) {
			resolution, err := s.tools.Counter(ctx, in.SessionID, in.AmountPaise)
			if err != nil {
				return counterResult{}, err
			}
			return counterResult{Status: resolution.Status, FinalAmountPaise: resolution.FinalAmountPaise}, nil
		})
	accept := mustTool("accept_offer", "Formally accept the merchant's current terms for this session.",
		func(ctx agent.Context, in sessionInput) (counterResult, error) {
			resolution, err := s.tools.Accept(ctx, in.SessionID)
			if err != nil {
				return counterResult{}, err
			}
			return counterResult{Status: resolution.Status, FinalAmountPaise: resolution.FinalAmountPaise}, nil
		})
	decline := mustTool("decline_offer", "Formally decline the merchant's current terms.",
		func(ctx agent.Context, in declineInput) (counterResult, error) {
			resolution, err := s.tools.Decline(ctx, in.SessionID, in.Reason)
			if err != nil {
				return counterResult{}, err
			}
			return counterResult{Status: resolution.Status, FinalAmountPaise: resolution.FinalAmountPaise}, nil
		})
	getTerms := mustTool("get_current_terms", "Read the latest terms of an open negotiation session.",
		func(ctx agent.Context, in sessionInput) (map[string]any, error) {
			return map[string]any{"session_id": in.SessionID}, nil
		})
	return []tool.Tool{counter, accept, decline, getTerms}
}

type counterInput struct {
	SessionID   string `json:"session_id"`
	AmountPaise int64  `json:"amount_paise"`
}

type sessionInput struct {
	SessionID string `json:"session_id"`
}

type declineInput struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type counterResult struct {
	Status           string `json:"status"`
	FinalAmountPaise int64  `json:"final_amount_paise"`
	Reason           string `json:"reason,omitempty"`
}

// classifyOffer routes an offer by hard money ceilings first, then the
// premium-band guidance given to the negotiating agent.
func classifyOffer(offer Offer, wallet Wallet) string {
	ceiling := wallet.BudgetPaise
	if ceiling <= 0 {
		ceiling = wallet.SpendLimitPaise
	}
	switch {
	case offer.FinalPaise > wallet.BalancePaise,
		ceiling > 0 && offer.FinalPaise > ceiling:
		return RouteDecline
	case offer.BasePaise > 0 &&
		(offer.FinalPaise-offer.BasePaise)*100 <= offer.BasePaise*AutoBuyPremiumMaxPct:
		return RouteAccept
	default:
		return RouteNegotiate
	}
}

func mustTool[TArgs, TResult any](name, description string, fn func(agent.Context, TArgs) (TResult, error)) tool.Tool {
	wrapped, err := functiontool.New(functiontool.Config{Name: name, Description: description},
		functiontool.Func[TArgs, TResult](fn))
	if err != nil {
		panic(fmt.Sprintf("shopgraph: build tool %s: %v", name, err))
	}
	return wrapped
}

// runnerFor builds a fresh in-memory runner per run so sessions never leak
// between Telegram messages.
func runnerFor(appName string, a agent.Agent) (*runner.Runner, error) {
	return runner.NewInMemory(appName, a)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func textContent(text string) *genai.Content { return genai.NewContentFromText(text, genai.RoleUser) }

func defaultRunConfig() agent.RunConfig { return agent.RunConfig{} }
