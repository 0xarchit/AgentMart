// Graph construction and runtime for AgentMart's buying agent.
//
// Layout (workflow engine):
//
//	START  to  intent(LLM)  to  search(catalog)  to  select(LLM)  to  offer(propose)
//	    offer --ACCEPT---> accept -------------
//	    offer --NEGOTIATE> negotiate(reasoning loop) -+-> finalize(verify)  to  END
//	    offer --DECLINE--> declined(outcome) --------> END
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

	"agentmart/internal/failure"
	"agentmart/internal/llmchat"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
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
	// MerchantAgent, when set, is the merchant's own agent reached as a remote agent.
	// The negotiating agent gets it as a delegate tool, so the deal is struck
	// as one agent talking to another instead of bespoke request glue.
	MerchantAgent agent.Agent
}

// Service owns the compiled graph and per-run wallet slot. The buyer bot is
// serial, so one slot is safe; revisit if runs ever fan out per user.
type Service struct {
	tools       Tools
	model       *llmchat.Model
	merchant    agent.Agent
	chooseAgent agent.Agent
	assessAgent agent.Agent
	mu          sync.Mutex
	wallet      Wallet
	progress    func(string)
	wfAgent     agent.Agent
}

// New compiles the graph. Safe to call without network: models are only used
// when a run executes.
func New(ctx context.Context, cfg Config, tools Tools) (*Service, error) {
	if err := tools.validate(); err != nil {
		return nil, err
	}
	s := &Service{tools: tools, model: llmchat.New(cfg.Model, cfg.APIKey, cfg.BaseURL), merchant: cfg.MerchantAgent}
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

// ponytail: one run at a time per service, which is what the message loop does.
// Give each run its own service if concurrent shoppers ever share one.
func (s *Service) setRun(w Wallet, progress func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallet = w
	s.progress = progress
}

// note reports which stage the run reached, so a slow or stuck step is visible
// while it happens rather than only in the final error.
func (s *Service) note(line string) {
	s.mu.Lock()
	report := s.progress
	s.mu.Unlock()
	if report != nil {
		report(line)
	}
}

func (s *Service) decisionModel() model.LLM { return s.model }

var (
	selectInstruction = `The shop has shown you what it has. Each option carries
the shop's own pitch, its price in paise, stock, warranty, and trust score.

Pick exactly one, the way a careful shopper would: does it match what was
asked for, is the price sensible for what is included, and is the shop's pitch
backed by the numbers. Prefer the option that genuinely suits the request over
the cheapest one, and say in one line why you chose it.

Return the chosen product_id, the quantity you want, and that reason. If
nothing on offer fits, return product_id "" and say why.`

	assessInstruction = fmt.Sprintf(`You are AgentMart's buyer, shopping for one
real person and spending their money. You are handed the merchant's offer plus
that person's money facts: wallet_balance_paise, spend_limit_paise,
budget_paise, premium_over_list_paise, premium_over_list_pct, and an advisory
advisory_band_pct=%d.

Decide what a careful shopper would do next and return only JSON:
{"decision":"accept|negotiate|ask_human|decline","reason":"one short sentence"}

How to think about it (guidance, not rules you must obey):
- "accept" when the total is fair for what is included and comfortably within
  the person's money.
- "negotiate" when the merchant is asking more than the value justifies, or a
  bundle is being pushed you think you can get cheaper.
- "ask_human" when the call is genuinely theirs to make: a big premium, a
  trade-off between price and warranty or bundle, or anything you would want a
  friend's nod on before spending their money.
- "decline" when it simply does not work for them.

Speak in the reason like a person explaining the choice to a friend, referring
to the actual amounts and what the offer includes.`, AutoBuyPremiumMaxPct)

	negotiateInstruction = fmt.Sprintf(`You are AgentMart's negotiating buyer.
You receive the merchant's opening offer (session_id, final_amount_paise,
base_amount_paise) plus wallet facts and premium_band_pct=%d. Decide:
- If the offer fits your constraints, call accept_offer(session_id).
- Otherwise call counter_offer(session_id, amount_paise) ONCE at about 85%% of
  the ask. Then judge the response: accept_offer if it now fits, decline_offer
  otherwise. Never exceed wallet_balance_paise or budget_paise.
- When the merchant is reachable you may consult it first: ask why the price
  holds, what the bundle adds, or whether a loyalty deal applies. Use its
  answer in your reasoning, but only counter_offer/accept_offer/decline_offer
  change the deal.
Every turn is a function call. Settle the deal first, then report what you did
through the final answer function. Never reply in prose.`, AutoBuyPremiumMaxPct)
)

// Per-node time bounds.
const (
	nodeTimeout      = 90 * time.Second
	assessTimeout    = 90 * time.Second
	negotiateTimeout = 180 * time.Second
)

// Route names. The buyer agent picks one; code only maps its choice onto an
// edge and refuses to spend money the user does not have.
const (
	RouteAccept    = "ACCEPT"
	RouteNegotiate = "NEGOTIATE"
	RouteAskHuman  = "ASK_HUMAN"
	RouteDecline   = "DECLINE"
)

// buildGraph wires every node and wraps the workflow as a standard agent.
func (s *Service) buildGraph() (agent.Agent, error) {
	// Each reasoning node answers in the shape of its result type. Without this
	// the provider replies in prose and the node's own validation rejects it.
	selectionSchema, err := llmchat.SchemaFor[Selection]()
	if err != nil {
		return nil, err
	}
	assessmentSchema, err := llmchat.SchemaFor[Assessment]()
	if err != nil {
		return nil, err
	}
	outcomeSchema, err := llmchat.SchemaFor[Outcome]()
	if err != nil {
		return nil, err
	}

	selectAgent, err := llmagent.New(llmagent.Config{
		Name: "select-agent", Model: s.decisionModel(), Instruction: selectInstruction,
		OutputSchema: selectionSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("select agent: %w", err)
	}
	s.chooseAgent = selectAgent
	negotiateAgent, err := llmagent.New(llmagent.Config{
		Name:         "negotiate-agent",
		Model:        s.decisionModel(),
		Instruction:  negotiateInstruction,
		Tools:        s.negotiationTools(),
		OutputSchema: outcomeSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("negotiate agent: %w", err)
	}
	assessAgent, err := llmagent.New(llmagent.Config{
		Name: "assess-agent", Model: s.decisionModel(), Instruction: assessInstruction,
		OutputSchema: assessmentSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("assess agent: %w", err)
	}
	s.assessAgent = assessAgent

	// The shop conversation opens here: the buyer says what it wants and the
	// merchant answers with its own pick of stock, pitched in its own words.
	askShopNode := workflow.NewFunctionNode[string, negotiationclient.Shortlist]("ask_shop",
		func(ctx agent.Context, request string) (negotiationclient.Shortlist, error) {
			wallet := s.walletSnapshot()
			budget := wallet.SpendLimitPaise
			brief := strings.TrimSpace(request)
			if brief == "" {
				return negotiationclient.Shortlist{}, fmt.Errorf("nothing to ask the shop for")
			}
			s.note(fmt.Sprintf("Asking the shop about %q, up to INR %.2f", brief, float64(budget)/100))
			shortlist, err := s.tools.Browse(ctx, brief, budget, wallet.AccountID)
			if err != nil {
				return negotiationclient.Shortlist{}, err
			}
			if len(shortlist.Options) == 0 {
				return negotiationclient.Shortlist{}, fmt.Errorf("the shop had nothing to show for %q", brief)
			}
			return shortlist, nil
		}, workflow.NodeConfig{Timeout: negotiateTimeout})

	// Choosing runs inline so the opening turns keep travelling with the choice.
	chooseNode := workflow.NewFunctionNode[negotiationclient.Shortlist, Pick]("choose_option",
		func(ctx agent.Context, shortlist negotiationclient.Shortlist) (Pick, error) {
			names := make([]string, 0, len(shortlist.Options))
			for _, option := range shortlist.Options {
				names = append(names, fmt.Sprintf("%s at INR %.2f", option.Name, float64(option.PricePaise)/100))
			}
			s.note("The shop showed: " + strings.Join(names, "; "))
			selection, err := s.choose(ctx, shortlist)
			if err != nil {
				return Pick{}, err
			}
			if strings.TrimSpace(selection.ProductID) == "" {
				return Pick{}, fmt.Errorf("buyer agent chose nothing: %s", selection.Rationale)
			}
			if selection.Quantity <= 0 {
				selection.Quantity = 1
			}
			s.note(fmt.Sprintf("Chose %s: %s", selection.ProductID, selection.Rationale))
			return Pick{Selection: selection, ShopTranscript: shortlist.Transcript}, nil
		}, workflow.NodeConfig{Timeout: assessTimeout})

	offerNode := workflow.NewFunctionNode[Pick, OfferView]("fetch_offer",
		func(ctx agent.Context, pick Pick) (OfferView, error) {
			wallet := s.walletSnapshot()
			s.note("Asking the shop to price it")
			proposal, err := s.tools.Offers(ctx, pick.ProductID, pick.Quantity, wallet.AccountID)
			if err != nil {
				return OfferView{}, err
			}
			offer := Offer{
				SessionID: proposal.SessionID, ProductID: proposal.ProductID,
				ProductName: proposal.Name, Quantity: proposal.Quantity,
				BasePaise: proposal.BaseAmountPaise, FinalPaise: proposal.FinalAmountPaise,
				Reason:     proposal.Reason,
				Transcript: proposal.Transcript,
				ShopTurns:  pick.ShopTranscript,
			}
			premium := offer.FinalPaise - offer.BasePaise
			view := OfferView{
				Offer:              offer,
				WalletBalancePaise: wallet.BalancePaise,
				SpendLimitPaise:    wallet.SpendLimitPaise,
				BudgetPaise:        wallet.BudgetPaise,
				PremiumPaise:       premium,
				AdvisoryBandPct:    AutoBuyPremiumMaxPct,
			}
			if offer.BasePaise > 0 {
				view.PremiumPct = int(premium * 100 / offer.BasePaise)
			}
			return view, nil
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	// One node judges and routes. The offer and the judgement stay in the same
	// scope, so nothing has to look up what an earlier node left behind. The
	// judgement itself is the agent's: the only override here is a money guard.
	//
	// The output type is any because only a genuinely nil return suppresses the
	// node's own terminal event, and the routed event we emit already carries the
	// offer. Returning a zero Offer would count as a second output value.
	decideNode := workflow.NewEmittingFunctionNode[OfferView, any]("decide_offer",
		func(ctx agent.Context, view OfferView, emit func(*session.Event) error) (any, error) {
			if view.SessionID == "" {
				return nil, fmt.Errorf("merchant returned no negotiation session for %q", view.ProductID)
			}
			s.note(fmt.Sprintf("The shop quoted %s at INR %.2f: %s",
				view.ProductName, float64(view.FinalPaise)/100, view.Offer.Reason))
			assessment, err := s.assess(ctx, view)
			if err != nil {
				// The quote is already in hand. Losing the judgement is not a
				// reason to lose the run: hand the offer to the person, and say
				// which layer failed rather than hiding it behind a guess.
				explanation := failure.Explain(err)
				s.note("Could not judge this offer, so it goes to you. " + explanation)
				assessment = Assessment{
					Decision: "ask_human",
					Reason:   "the buyer agent could not judge this offer: " + firstLine(explanation),
				}
			}
			offer := view.Offer
			route, note := routeFor(assessment, offer, s.walletSnapshot())
			s.note("Decision: " + route + ". " + joinReason(assessment.Reason, note))
			offer.Route = route
			offer.Reason = joinReason(assessment.Reason, note, offer.Reason)
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{route}
			ev.Output = offer
			if err := emit(ev); err != nil {
				return nil, err
			}
			return nil, nil // the routed edge carries ev.Output forward
		}, workflow.NodeConfig{Timeout: nodeTimeout + assessTimeout})

	acceptNode := workflow.NewFunctionNode[Offer, Outcome]("accept_offer",
		func(ctx agent.Context, offer Offer) (Outcome, error) {
			resolution, err := s.tools.Accept(ctx, offer.SessionID)
			if err != nil {
				return Outcome{}, err
			}
			out := outcomeFrom(offer, resolution)
			out.Action = string(ActionBuy)
			out.Accepted = true
			out.Rationale = joinReason(offer.Reason, "buyer agent accepted the merchant's terms")
			return out, nil
		}, workflow.NodeConfig{Timeout: nodeTimeout})

	// The agent asked for the human. The negotiation session stays open on purpose: the
	// deal is genuinely pending a person, not accepted and not refused.
	askHumanNode := workflow.NewFunctionNode[Offer, Outcome]("ask_human",
		func(ctx agent.Context, offer Offer) (Outcome, error) {
			out := outcomeFrom(offer, negotiationclient.Resolution{
				SessionID: offer.SessionID, FinalAmountPaise: offer.FinalPaise,
			})
			out.Action = string(ActionAskHuman)
			out.Status = "needs_human"
			out.Rationale = joinReason(offer.Reason, "buyer agent escalated this offer to the human")
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
		Add(workflow.Start, askShopNode).
		Add(askShopNode, chooseNode).
		Add(chooseNode, offerNode).
		Add(offerNode, decideNode)
	builder.AddRoutes(decideNode, map[string]workflow.Node{
		RouteAccept:    acceptNode,
		RouteNegotiate: negotiateNode,
		RouteAskHuman:  askHumanNode,
		RouteDecline:   declinedNode,
	})
	// The engine requires branch convergence via JoinNode.
	join := workflow.NewJoinNode("negotiation_join")
	builder.Add(negotiateNode, join).
		Add(acceptNode, join).
		Add(askHumanNode, join).
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
			action := Action(outcome.Action)
			if action == "" {
				action = ActionBuy
			}
			bandCrossed := baseMain > 0 && premium > 0 && premium*100 > baseMain*AutoBuyPremiumMaxPct
			needsApproval := action == ActionAskHuman || bandCrossed
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
// verified result. Errors surface the real cause, strict mode.
func (s *Service) Run(parent context.Context, request string, wallet Wallet) (Result, error) {
	return s.RunWithProgress(parent, request, wallet, nil)
}

// RunWithProgress runs the graph and reports each stage as it starts. The
// progress function may be nil.
func (s *Service) RunWithProgress(parent context.Context, request string, wallet Wallet, progress func(string)) (Result, error) {
	if s.wfAgent == nil {
		return Result{}, fmt.Errorf("shop graph is not built")
	}
	s.setRun(wallet, progress)

	runner, err := runnerFor("shop", s.wfAgent)
	if err != nil {
		return Result{}, err
	}
	// The graph's first stop is the shop, and what the shop needs is the person's
	// own words. The money facts are read from the wallet at the node that needs
	// them, so they never travel as prose.
	runCtx, cancel := context.WithTimeout(parent, graphRunDeadline)
	defer cancel()

	var lastOutput any
	var lastResult *Result
	events := 0
	for event, runErr := range runner.Run(runCtx, "user", fmt.Sprintf("run-%d", time.Now().UnixNano()),
		textContent(request), defaultRunConfig()) {
		if runErr != nil {
			return Result{}, fmt.Errorf("graph run failed after %d events: %w", events, runErr)
		}
		events++
		if events > maxGraphEvents {
			return Result{}, fmt.Errorf("graph exceeded %d events without finishing", maxGraphEvents)
		}
		if event != nil && event.Output != nil {
			lastOutput = event.Output
			// The last node verifies the product against the catalog and decides
			// whether a person must be asked. Prefer its answer: rebuilding one
			// here would drop both.
			if finalized, ok := event.Output.(Result); ok {
				lastResult = &finalized
			}
		}
	}
	return s.resultFrom(lastResult, lastOutput)
}

// resultFrom turns what the graph finished holding into a result. A settled
// outcome is used as is; a quote that nothing settled becomes the person's call
// rather than a lost run.
func (s *Service) resultFrom(lastResult *Result, lastOutput any) (Result, error) {
	if lastResult != nil {
		return normalized(*lastResult), nil
	}
	if outcome, ok := lastOutput.(Outcome); ok {
		return normalized(Result{
			Action: Action(outcome.Action), ProductID: outcome.ProductID,
			ProductName: outcome.ProductName, Quantity: outcome.Quantity,
			FinalPaise: outcome.FinalPaise, Rationale: outcome.Rationale,
			Steps: outcome.Steps, SessionID: outcome.SessionID,
			Transcript: outcome.Transcript, Accepted: outcome.Accepted,
			NeedsApproval: Action(outcome.Action) == ActionAskHuman,
		}), nil
	}
	// The quote is in hand but nothing settled it. Losing the negotiation is not
	// a reason to lose the run: hand the offer to the person, who can still say
	// yes. Escalation never spends, so this stays money safe.
	if offer, ok := lastOutput.(Offer); ok {
		s.note("The negotiation did not settle, so this offer goes to you.")
		return normalized(Result{
			Action: ActionAskHuman, ProductID: offer.ProductID, ProductName: offer.ProductName,
			Quantity: offer.Quantity, FinalPaise: offer.FinalPaise, SessionID: offer.SessionID,
			Rationale:  joinReason(offer.Reason, "the negotiation did not settle, so it goes to you"),
			Transcript: offer.ShopTurns,
		}), nil
	}
	return Result{}, fmt.Errorf("graph finished without a result (last output %T)", lastOutput)
}

// normalized fills the defaults a caller can rely on.
func normalized(result Result) Result {
	if result.Quantity <= 0 {
		result.Quantity = 1
	}
	if result.Action == "" {
		result.Action = ActionBuy
	}
	if result.Action == ActionAskHuman {
		result.NeedsApproval = true
	}
	return result
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

// outcomeFrom projects a merchant resolution onto the stage contract.
func outcomeFrom(offer Offer, resolution negotiationclient.Resolution) Outcome {
	settled := resolution.Transcript
	if len(settled) == 0 {
		settled = offer.Transcript
	}
	out := Outcome{
		Status:      resolution.Status,
		ProductID:   offer.ProductID,
		ProductName: offer.ProductName,
		Quantity:    offer.Quantity,
		SessionID:   resolution.SessionID,
		FinalPaise:  resolution.FinalAmountPaise,
		Transcript:  append(append([]negotiation.Turn{}, offer.ShopTurns...), settled...),
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
	graphRunDeadline = 600 * time.Second
	maxGraphEvents   = 200
)

// negotiationTools exposes the negotiation moves to the negotiate agent.
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
	negotiationTools := []tool.Tool{counter, accept, decline, getTerms}
	// Agent-to-agent delegation: when the merchant's own agent is reachable over
	// as a remote agent, expose it as a tool so the buyer can ask it to justify terms, pitch
	// bundles, or respond to a counter in its own words.
	if s.merchant != nil {
		negotiationTools = append(negotiationTools,
			agenttool.New(s.merchant, &agenttool.Config{SkipSummarization: false}))
	}
	return negotiationTools
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

// classifyOffer is gone: the buyer agent decides accept/negotiate/ask_human/
// decline. routeFor only carries that decision onto an edge, and refuses to let
// an "accept" spend money the user does not have, escalating to the human
// rather than quietly overruling the agent.
func routeFor(assessment Assessment, offer Offer, wallet Wallet) (string, string) {
	route := ""
	switch strings.ToLower(strings.TrimSpace(assessment.Decision)) {
	case "accept", "buy", "accept_offer":
		route = RouteAccept
	case "negotiate", "counter", "counter_offer":
		route = RouteNegotiate
	case "ask_human", "ask-human", "askhuman", "human", "confirm":
		route = RouteAskHuman
	case "decline", "reject", "skip":
		route = RouteDecline
	default:
		return RouteAskHuman, fmt.Sprintf("agent returned an unclear decision %q, so the human decides", assessment.Decision)
	}

	if route != RouteAccept {
		return route, ""
	}
	ceiling := wallet.BudgetPaise
	if ceiling <= 0 {
		ceiling = wallet.SpendLimitPaise
	}
	switch {
	case offer.FinalPaise > wallet.BalancePaise:
		return RouteAskHuman, fmt.Sprintf("wallet holds INR %.2f but the offer is INR %.2f, so the human decides",
			float64(wallet.BalancePaise)/100, float64(offer.FinalPaise)/100)
	case ceiling > 0 && offer.FinalPaise > ceiling:
		return RouteAskHuman, fmt.Sprintf("offer INR %.2f is above the stated limit INR %.2f, so the human decides",
			float64(offer.FinalPaise)/100, float64(ceiling)/100)
	default:
		return RouteAccept, ""
	}
}

// firstLine keeps an explanation short enough to sit inside a reason.
func firstLine(text string) string {
	if index := strings.IndexByte(text, byte('\n')); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return strings.TrimSpace(text)
}

// joinReason strings together the non-empty parts of an explanation.
func joinReason(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "; ")
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
// choose asks the buyer agent which of the shop's options to take. Run inline
// so the shortlist and the choice never leave the same scope.
func (s *Service) choose(parent context.Context, shortlist negotiationclient.Shortlist) (Selection, error) {
	if s.chooseAgent == nil {
		return Selection{}, fmt.Errorf("choosing agent is not built")
	}
	payload, err := jsonMarshal(shortlist)
	if err != nil {
		return Selection{}, err
	}
	run, err := runnerFor("shop-choose", s.chooseAgent)
	if err != nil {
		return Selection{}, fmt.Errorf("choosing runner: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, assessTimeout)
	defer cancel()

	var last *session.Event
	for ev, runErr := range run.Run(ctx, "user", fmt.Sprintf("choose-%d", time.Now().UnixNano()),
		textContent(string(payload)), defaultRunConfig()) {
		if runErr != nil {
			return Selection{}, fmt.Errorf("choose an option: %w", runErr)
		}
		if ev != nil {
			last = ev
		}
	}
	return selectionFrom(last)
}

func selectionFrom(ev *session.Event) (Selection, error) {
	if ev == nil {
		return Selection{}, fmt.Errorf("choosing agent produced no answer")
	}
	if encoded, err := json.Marshal(ev.Output); err == nil {
		var out Selection
		if json.Unmarshal(encoded, &out) == nil && out.ProductID != "" {
			return out, nil
		}
	}
	if ev.Content != nil {
		for _, part := range ev.Content.Parts {
			if part == nil || strings.TrimSpace(part.Text) == "" {
				continue
			}
			var out Selection
			if err := json.Unmarshal([]byte(part.Text), &out); err == nil && out.ProductID != "" {
				return out, nil
			}
		}
	}
	return Selection{}, fmt.Errorf("choosing agent named no product")
}

// assess asks the buyer agent to judge one offer. It runs inside the deciding
// node so the offer and the judgement never leave the same scope.
func (s *Service) assess(parent context.Context, view OfferView) (Assessment, error) {
	if s.assessAgent == nil {
		return Assessment{}, fmt.Errorf("assessing agent is not built")
	}
	payload, err := jsonMarshal(view)
	if err != nil {
		return Assessment{}, err
	}
	run, err := runnerFor("shop-assess", s.assessAgent)
	if err != nil {
		return Assessment{}, fmt.Errorf("assessing runner: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, assessTimeout)
	defer cancel()

	var last *session.Event
	for ev, runErr := range run.Run(ctx, "user", fmt.Sprintf("assess-%d", time.Now().UnixNano()),
		textContent(string(payload)), defaultRunConfig()) {
		if runErr != nil {
			return Assessment{}, fmt.Errorf("assess offer: %w", runErr)
		}
		if ev != nil {
			last = ev
		}
	}
	return assessmentFrom(last)
}

// assessmentFrom reads the judgement out of the agent's last event, whether the
// framework already parsed it or left it as text.
func assessmentFrom(ev *session.Event) (Assessment, error) {
	if ev == nil {
		return Assessment{}, fmt.Errorf("assessing agent produced no answer")
	}
	switch typed := ev.Output.(type) {
	case Assessment:
		return typed, nil
	case map[string]any:
		if encoded, err := json.Marshal(typed); err == nil {
			var out Assessment
			if json.Unmarshal(encoded, &out) == nil && out.Decision != "" {
				return out, nil
			}
		}
	}
	if ev.Content != nil {
		for _, part := range ev.Content.Parts {
			if part == nil || strings.TrimSpace(part.Text) == "" {
				continue
			}
			var out Assessment
			if err := json.Unmarshal([]byte(part.Text), &out); err == nil && out.Decision != "" {
				return out, nil
			}
		}
	}
	return Assessment{}, fmt.Errorf("assessing agent answered without a decision")
}

func runnerFor(appName string, a agent.Agent) (*runner.Runner, error) {
	return runner.NewInMemory(appName, a)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func textContent(text string) *genai.Content { return genai.NewContentFromText(text, genai.RoleUser) }

func defaultRunConfig() agent.RunConfig { return agent.RunConfig{} }
