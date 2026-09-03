// Graph construction and runtime for AgentMart's buying agent.
//
// Layout (workflow engine):
//
//	START  to  ask_shop(conversation)  to  choose_option(reasoning)  to  fetch_offer(quote)
//	    decide_offer --ACCEPT----> accept -------------
//	    decide_offer --NEGOTIATE-> negotiate(reasoning) -+-> finalize(verify)  to  END
//	    decide_offer --ASK_HUMAN-> ask_human ----------/
//	    decide_offer --DECLINE---> declined -----------/
//
// Every judgement is an agent's; routing, floors, and caps are deterministic.
package shopgraph

import (
	"context"
	"encoding/json"
	"errors"
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

// Service owns the compiled graph. Each run keeps its own money facts and its
// own progress reporter, because two callers can be shopping at once: the chat
// loop and the public buyer surface share one service.
type Service struct {
	tools       Tools
	model       *llmchat.Model
	merchant    agent.Agent
	chooseAgent agent.Agent
	assessAgent agent.Agent
	// runs holds each caller's money facts against the session its own graph pass
	// runs under. One shared slot let an outside caller's stated budget replace a
	// person's real spend limit mid run, which decides whether the run asks them.
	runs    sync.Map
	wfAgent agent.Agent
}

// runState is what one graph pass needs that is not in its input.
type runState struct {
	wallet   Wallet
	progress func(string)
	prior    Conversation
	mu       sync.Mutex
	shown    []PriorOption
	priced   pricedGoods
}

// pricedGoods is the basket the shop quoted on one pass: the goods, how many, and
// what was attached. The negotiating agent settles the price of this basket, so it
// is deliberately not the agent's to change: none of these three fields is in its
// output schema, and settlement reads them back from here.
type pricedGoods struct {
	productID    string
	quantity     int
	bundledPaise int64
}

// PriorOption is one item the shop showed earlier, kept so a follow up such as
// "the second one" has something to refer to.
type PriorOption struct {
	ProductID  string `json:"product_id"`
	Name       string `json:"name"`
	PricePaise int64  `json:"price_paise"`
}

// Conversation is what was already said between this person and the shop. It
// carries no money facts: the wallet, the limits and the gate are read fresh
// every run, so a stale conversation can widen nothing.
type Conversation struct {
	Brief   string        `json:"original_request"`
	Options []PriorOption `json:"options_already_shown,omitempty"`
	Chosen  string        `json:"product_they_were_quoted,omitempty"`
}

// Empty reports whether there is nothing worth telling the agents about.
func (c Conversation) Empty() bool {
	return strings.TrimSpace(c.Brief) == "" && len(c.Options) == 0
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

// walletFor returns the money facts belonging to the graph pass this node is
// running inside, identified by its own session rather than by whatever ran last.
// An unknown session yields a zero wallet, which can approve nothing.
func (s *Service) walletFor(sessionID string) Wallet {
	state, ok := s.runs.Load(sessionID)
	if !ok {
		return Wallet{}
	}
	run, ok := state.(*runState)
	if !ok || run == nil {
		return Wallet{}
	}
	return run.wallet
}

// begin records the facts for one run. end must be called when it finishes.
func (s *Service) begin(sessionID string, w Wallet, progress func(string), prior Conversation) {
	s.runs.Store(sessionID, &runState{wallet: w, progress: progress, prior: prior})
}

// priorFor returns what was already said in this conversation, or an empty
// conversation when this is the first message.
func (s *Service) priorFor(sessionID string) Conversation {
	run := s.runFor(sessionID)
	if run == nil {
		return Conversation{}
	}
	return run.prior
}

// runFor finds one run's state, or nil when the run is unknown.
func (s *Service) runFor(sessionID string) *runState {
	state, ok := s.runs.Load(sessionID)
	if !ok {
		return nil
	}
	run, ok := state.(*runState)
	if !ok {
		return nil
	}
	return run
}

// recordShown keeps what the shop put in front of the person on this pass.
func (s *Service) recordShown(sessionID string, options []PriorOption) {
	run := s.runFor(sessionID)
	if run == nil {
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.shown = options
}

// shownFor reports what the shop showed on this pass, if it got that far.
func (s *Service) shownFor(sessionID string) []PriorOption {
	run := s.runFor(sessionID)
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.shown
}

// recordPriced keeps the basket the shop actually quoted on this pass, so the
// settled outcome can be held to it.
func (s *Service) recordPriced(sessionID string, goods pricedGoods) {
	run := s.runFor(sessionID)
	if run == nil {
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.priced = goods
}

// pricedFor reports the basket this pass was quoted for. A zero value means the
// run never got as far as a price.
func (s *Service) pricedFor(sessionID string) pricedGoods {
	run := s.runFor(sessionID)
	if run == nil {
		return pricedGoods{}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.priced
}

// end releases one run's facts.
func (s *Service) end(sessionID string) {
	s.runs.Delete(sessionID)
}

// noteTo reports which stage a run reached, so a slow or stuck step is visible
// while it happens rather than only in the final error. A note for a run nobody
// is watching is dropped rather than delivered to another caller.
func (s *Service) noteTo(sessionID, line string) {
	state, ok := s.runs.Load(sessionID)
	if !ok {
		return
	}
	run, ok := state.(*runState)
	if !ok || run == nil || run.progress == nil {
		return
	}
	run.progress(line)
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
nothing on offer fits, return product_id "" and say why.

When earlier_in_this_conversation is present, this is a follow up rather than a
fresh request. It holds what this person originally asked for and the options
they were already shown, in the order they saw them, so a reference such as "the
second one" or "the cheaper one" points at a real product. Honour what they now
say over what they said first, and resolve the reference against that list before
reading it as a new request. Their words are a preference, never permission to
spend: the amount is still decided later.`

	assessInstruction = fmt.Sprintf(`You are AgentMart's buyer, shopping for one
real person and spending their money. You are handed the merchant's offer plus
that person's money facts: wallet_balance_paise, spend_limit_paise,
budget_paise, premium_over_list_paise, premium_over_list_pct, and an advisory
advisory_band_pct=%d.

Decide what a careful shopper would do next and return only JSON:
{"decision":"accept|negotiate|ask_human|decline","reason":"one short sentence"}

How to think about it (guidance, not rules you must obey):
- "accept" when the total is fair for what is included and comfortably within
  the person's money. An offer whose premium_over_list_pct is inside
  advisory_band_pct and whose total is inside spend_limit_paise does not need a
  person: the band and the limit already made that call, and handing it over
  anyway spends their attention on a decision they have already delegated.
- "negotiate" when the merchant is asking more than the value justifies, or a
  bundle is being pushed you think you can get cheaper.
- "ask_human" when the numbers themselves say so: premium_over_list_pct sits
  outside advisory_band_pct, the total is above spend_limit_paise or
  wallet_balance_paise, or the money facts genuinely conflict with the brief.
- "decline" when it simply does not work for them.

premium_over_list_paise already counts anything attached to the main product as
part of the list value, so a bundle is not markup and a fair bundle is not by
itself a reason to ask.

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

// errNothingToShow means the shop had nothing inside the budget. A shop with an
// empty shelf is an answer, not a fault, so a run ending this way declines
// rather than failing.
var errNothingToShow = errors.New("the shop had nothing within the budget")

// errNothingWorthBuying means the buyer looked at the shortlist and picked
// nothing from it. Refusing everything on offer is a judgement, not a fault, so
// a run ending this way declines rather than failing.
var errNothingWorthBuying = errors.New("nothing on the shelf was worth buying")

// shownOption resolves a chosen product id to what the person was actually shown,
// so progress notes read as a product rather than an identifier and a choice
// nobody was offered can be refused rather than priced. An earlier pass of the
// same conversation counts: a follow up such as "the second one" refers to what
// was on screen then, and re-browsing for it can come back with a different
// shortlist.
func shownOption(shortlist negotiationclient.Shortlist, prior Conversation, productID string) (string, bool) {
	wanted := strings.TrimSpace(productID)
	for _, option := range shortlist.Options {
		if strings.TrimSpace(option.ProductID) == wanted {
			return option.Name, true
		}
	}
	for _, option := range prior.Options {
		if strings.TrimSpace(option.ProductID) == wanted {
			return option.Name, true
		}
	}
	return productID, false
}

// pickFrom turns a model's answer into the choice the rest of the run works from,
// or refuses it. It sits outside the node it came from so the refusals can be
// exercised without a model: a blank answer, and an id nobody was shown.
func (s *Service) pickFrom(sessionID string, shortlist negotiationclient.Shortlist, prior Conversation, selection Selection) (Pick, error) {
	// Normalised here rather than compared loosely below, so the id that travels
	// on to be priced is the one the shop can look up.
	selection.ProductID = strings.TrimSpace(selection.ProductID)
	if selection.ProductID == "" {
		// Wrapped rather than plain, because a buyer that looked at the shelf and
		// explained why nothing fits has made a judgement, not failed. Only the
		// sentinel reaches the decline branch in the run loop; a plain error here
		// scored the most deliberate refusal there is as a lost run.
		return Pick{}, fmt.Errorf("%w: %s", errNothingWorthBuying, selection.Rationale)
	}
	// The choice has to be something the person was actually shown. A model
	// answering with a catalog id nobody offered would otherwise be priced,
	// judged and bought: the gate re-derives the amount from the catalog, so
	// the money would come out right for a product that was never on screen.
	// Non-blank was the only check here before, which caught an empty answer
	// and nothing else.
	name, shown := shownOption(shortlist, prior, selection.ProductID)
	if !shown {
		return Pick{}, fmt.Errorf("buyer agent chose %q, which the shop did not show", selection.ProductID)
	}
	if selection.Quantity <= 0 {
		selection.Quantity = 1
	}
	s.noteTo(sessionID, fmt.Sprintf("Chose %s: %s", name, selection.Rationale))
	return Pick{Selection: selection, ShopTranscript: shortlist.Transcript}, nil
}

// premiumOver reports what an ask adds over the list value of everything it
// includes. The list value is the main product plus any attached goods, because
// measuring against the main product alone counts a whole second product as
// markup and sends fair bundles to the person for no reason.
func premiumOver(finalPaise, listPaise int64) (paise int64, pct int) {
	paise = finalPaise - listPaise
	if listPaise > 0 {
		pct = int(paise * 100 / listPaise)
	}
	return paise, pct
}

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
			wallet := s.walletFor(ctx.SessionID())
			budget := wallet.SpendLimitPaise
			brief := strings.TrimSpace(request)
			if brief == "" {
				return negotiationclient.Shortlist{}, fmt.Errorf("nothing to ask the shop for")
			}
			// A follow up is asked against what was already discussed, so the shop
			// answers the refinement rather than treating it as a brief of its own.
			// The prior words are assembled here rather than by a model, so nothing
			// invented can enter the request.
			brief = briefWithHistory(brief, s.priorFor(ctx.SessionID()))
			s.noteTo(ctx.SessionID(), fmt.Sprintf("Asking the shop about %q, up to INR %.2f", brief, float64(budget)/100))
			shortlist, err := s.tools.Browse(ctx, brief, budget, wallet.AccountID)
			if err != nil {
				return negotiationclient.Shortlist{}, err
			}
			if len(shortlist.Options) == 0 {
				return negotiationclient.Shortlist{}, fmt.Errorf("%w for %q", errNothingToShow, brief)
			}
			shown := make([]PriorOption, 0, len(shortlist.Options))
			for _, option := range shortlist.Options {
				shown = append(shown, PriorOption{ProductID: option.ProductID, Name: option.Name, PricePaise: option.PricePaise})
			}
			s.recordShown(ctx.SessionID(), shown)
			return shortlist, nil
		}, workflow.NodeConfig{Timeout: negotiateTimeout})

	// Choosing runs inline so the opening turns keep travelling with the choice.
	chooseNode := workflow.NewFunctionNode[negotiationclient.Shortlist, Pick]("choose_option",
		func(ctx agent.Context, shortlist negotiationclient.Shortlist) (Pick, error) {
			names := make([]string, 0, len(shortlist.Options))
			for _, option := range shortlist.Options {
				names = append(names, fmt.Sprintf("%s at INR %.2f", option.Name, float64(option.PricePaise)/100))
			}
			s.noteTo(ctx.SessionID(), "The shop showed: "+strings.Join(names, "; "))
			prior := s.priorFor(ctx.SessionID())
			selection, err := s.choose(ctx, shortlist, prior)
			if err != nil {
				return Pick{}, err
			}
			return s.pickFrom(ctx.SessionID(), shortlist, prior, selection)
		}, workflow.NodeConfig{Timeout: assessTimeout})

	offerNode := workflow.NewFunctionNode[Pick, OfferView]("fetch_offer",
		func(ctx agent.Context, pick Pick) (OfferView, error) {
			wallet := s.walletFor(ctx.SessionID())
			s.noteTo(ctx.SessionID(), "Asking the shop to price it")
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
				QuotedAt:   time.Now().UTC(),
			}
			if proposal.Bundle != nil {
				offer.BundledPaise = proposal.Bundle.PricePaise * int64(offer.Quantity)
			}
			// The shop has now named, counted and priced one basket. Settlement is
			// held to it, so a later stage cannot swap the goods, change how many, or
			// invent attached goods that would flatter the premium band.
			s.recordPriced(ctx.SessionID(), pricedGoods{
				productID: offer.ProductID, quantity: offer.Quantity, bundledPaise: offer.BundledPaise,
			})
			premium, pct := premiumOver(offer.FinalPaise, offer.BasePaise+offer.BundledPaise)
			view := OfferView{
				Offer:              offer,
				WalletBalancePaise: wallet.BalancePaise,
				SpendLimitPaise:    wallet.SpendLimitPaise,
				BudgetPaise:        wallet.BudgetPaise,
				PremiumPaise:       premium,
				PremiumPct:         pct,
				AdvisoryBandPct:    AutoBuyPremiumMaxPct,
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
			s.noteTo(ctx.SessionID(), fmt.Sprintf("The shop quoted %s at INR %.2f: %s",
				view.ProductName, float64(view.FinalPaise)/100, view.Offer.Reason))
			assessment, err := s.assess(ctx, view)
			if err != nil {
				// The quote is already in hand. Losing the judgement is not a
				// reason to lose the run: hand the offer to the person, and say
				// which layer failed rather than hiding it behind a guess.
				explanation := failure.Explain(err)
				s.noteTo(ctx.SessionID(), "Could not judge this offer, so it goes to you. "+explanation)
				assessment = Assessment{
					Decision: "ask_human",
					Reason:   "the buyer agent could not judge this offer: " + firstLine(explanation),
				}
			}
			offer := view.Offer
			route, note := routeFor(assessment, offer, s.walletFor(ctx.SessionID()))
			s.noteTo(ctx.SessionID(), "Decision: "+route+". "+joinReason(assessment.Reason, note))
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
			goods := s.settledGoods(ctx.SessionID(), outcome)
			product, err := s.tools.Get(ctx, goods.productID)
			if err != nil {
				return Result{}, fmt.Errorf("verify candidate: %w", err)
			}
			qty := goods.quantity
			if qty <= 0 {
				qty = 1
			}
			listPaise := product.PricePaise*int64(qty) + goods.bundledPaise
			premium, _ := premiumOver(outcome.FinalPaise, listPaise)
			action := Action(outcome.Action)
			if action == "" {
				action = ActionBuy
			}
			bandCrossed := listPaise > 0 && premium > 0 && premium*100 > listPaise*AutoBuyPremiumMaxPct
			needsApproval := action == ActionAskHuman || bandCrossed
			return Result{
				Action: action, ProductID: product.ID, ProductName: product.Name,
				Quantity: qty, FinalPaise: outcome.FinalPaise, Rationale: outcome.Rationale,
				Steps: outcome.Steps, SessionID: outcome.SessionID, Transcript: outcome.Transcript,
				Accepted: outcome.Accepted, NeedsApproval: needsApproval,
				QuotedAt: outcome.QuotedAt,
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
	return s.ContinueWithProgress(parent, request, Conversation{}, wallet, progress)
}

// ContinueWithProgress runs the graph with what was already said in this
// conversation, so a follow up such as "the second one" continues rather than
// restarts. The prior conversation reaches the agents as context only: every
// money fact is still read fresh from the wallet, and the gate re-derives the
// amount either way, so nothing carried forward can widen a bound.
func (s *Service) ContinueWithProgress(parent context.Context, request string, prior Conversation, wallet Wallet, progress func(string)) (result Result, err error) {
	if s.wfAgent == nil {
		return Result{}, fmt.Errorf("shop graph is not built")
	}
	// The session identifies this pass. Every node reads its own money facts and
	// reports its own progress against it, so a second caller shopping at the same
	// time cannot be priced against this wallet or told about this run.
	sessionID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	s.begin(sessionID, wallet, progress, prior)
	// What the shop showed outlives the run, so the next message can refer to it.
	// Attached on every path, including the ones that end early, because a run that
	// ended without buying is exactly when a person says "the second one".
	defer func() {
		result.Shown = s.shownFor(sessionID)
		s.end(sessionID)
	}()

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
	for event, runErr := range runner.Run(runCtx, "user", sessionID,
		textContent(request), defaultRunConfig()) {
		if runErr != nil {
			if errors.Is(runErr, errNothingToShow) {
				s.noteTo(sessionID, "The shop had nothing within the budget, so nothing was bought.")
				return Result{Action: ActionDecline, Quantity: 1, Rationale: runErr.Error()}, nil
			}
			if errors.Is(runErr, errNothingWorthBuying) {
				s.noteTo(sessionID, "Nothing the shop showed was worth buying, so nothing was bought.")
				return Result{Action: ActionDecline, Quantity: 1, Rationale: runErr.Error()}, nil
			}
			// The quote is already in hand, so whatever broke after it, the person
			// can still decide. Hand the offer over with the reason rather than
			// losing the run.
			if offer, ok := lastOutput.(Offer); ok {
				s.noteTo(sessionID, "The run could not finish, so this offer goes to you.")
				return s.escalate(offer, "the run could not finish: "+failure.Explain(runErr)), nil
			}
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
	return s.resultFrom(sessionID, lastResult, lastOutput)
}

// settledGoods is the basket a settled outcome is allowed to describe: whatever
// the shop actually quoted on this pass. The goods, the count and the attached
// amount are all out of the negotiating agent's output schema, so an outcome
// carrying them got them from a function node that read them off the offer. The
// run's own record still wins, and the outcome is only the fallback for a caller
// that never began a run.
func (s *Service) settledGoods(sessionID string, outcome Outcome) pricedGoods {
	if priced := s.pricedFor(sessionID); priced.productID != "" {
		return priced
	}
	return pricedGoods{productID: outcome.ProductID, quantity: outcome.Quantity, bundledPaise: outcome.BundledPaise}
}

// resultFrom turns what the graph finished holding into a result. A settled
// outcome is used as is; a quote that nothing settled becomes the person's call
// rather than a lost run.
func (s *Service) resultFrom(sessionID string, lastResult *Result, lastOutput any) (Result, error) {
	if lastResult != nil {
		return normalized(*lastResult), nil
	}
	if outcome, ok := lastOutput.(Outcome); ok {
		goods := s.settledGoods(sessionID, outcome)
		return normalized(Result{
			Action: Action(outcome.Action), ProductID: goods.productID,
			ProductName: outcome.ProductName, Quantity: goods.quantity,
			FinalPaise: outcome.FinalPaise, Rationale: outcome.Rationale,
			Steps: outcome.Steps, SessionID: outcome.SessionID,
			Transcript: outcome.Transcript, Accepted: outcome.Accepted,
			NeedsApproval: Action(outcome.Action) == ActionAskHuman,
			QuotedAt:      outcome.QuotedAt,
		}), nil
	}
	// The quote is in hand but nothing settled it. Losing the negotiation is not
	// a reason to lose the run: hand the offer to the person, who can still say
	// yes. Escalation never spends, so this stays money safe.
	if offer, ok := lastOutput.(Offer); ok {
		s.noteTo(sessionID, "The negotiation did not settle, so this offer goes to you.")
		return s.escalate(offer, "the negotiation did not settle, so it goes to you"), nil
	}
	return Result{}, fmt.Errorf("graph finished without a result (last output %T)", lastOutput)
}

// escalate hands a quote to the person, with why nothing closed it. Escalation
// never spends, so this stays money safe whatever went wrong upstream.
func (s *Service) escalate(offer Offer, why string) Result {
	return normalized(Result{
		Action: ActionAskHuman, ProductID: offer.ProductID, ProductName: offer.ProductName,
		Quantity: offer.Quantity, FinalPaise: offer.FinalPaise, SessionID: offer.SessionID,
		Rationale:  joinReason(offer.Reason, why),
		Transcript: offer.ShopTurns,
		QuotedAt:   offer.QuotedAt,
	})
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
		Status:       resolution.Status,
		ProductID:    offer.ProductID,
		ProductName:  offer.ProductName,
		Quantity:     offer.Quantity,
		BundledPaise: offer.BundledPaise,
		SessionID:    resolution.SessionID,
		FinalPaise:   resolution.FinalAmountPaise,
		QuotedAt:     offer.QuotedAt,
		Transcript:   append(append([]negotiation.Turn{}, offer.ShopTurns...), settled...),
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
	// The negotiate agent already carries the standing terms in the transcript that
	// counter_offer, accept_offer and decline_offer return, so it has nothing left to
	// read them with. A tool that echoed its own session id back was worse than
	// nothing: it invited the agent to believe it had checked.
	negotiationTools := []tool.Tool{counter, accept, decline}
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
// choicePayload hands the shortlist to the choosing agent together with what was
// already said, so a reference to something shown earlier can be resolved.
type choicePayload struct {
	negotiationclient.Shortlist
	Earlier *Conversation `json:"earlier_in_this_conversation,omitempty"`
}

// earlierOrNil omits the history entirely on a first message, so the agent is not
// handed an empty object to read meaning into.
func earlierOrNil(prior Conversation) *Conversation {
	if prior.Empty() {
		return nil
	}
	return &prior
}

// briefWithHistory states the follow up together with what came before it. The
// person's new words lead, because a refinement replaces the part of the request
// it contradicts.
func briefWithHistory(brief string, prior Conversation) string {
	if prior.Empty() {
		return brief
	}
	var asked strings.Builder
	asked.WriteString(brief)
	if original := strings.TrimSpace(prior.Brief); original != "" && original != brief {
		fmt.Fprintf(&asked, ". This follows on from %q", original)
	}
	if len(prior.Options) > 0 {
		shown := make([]string, 0, len(prior.Options))
		for _, option := range prior.Options {
			shown = append(shown, fmt.Sprintf("%s at INR %.2f", option.Name, float64(option.PricePaise)/100))
		}
		fmt.Fprintf(&asked, ", after being shown %s", strings.Join(shown, "; "))
	}
	return asked.String()
}

// choose asks the buyer to pick one option, with the earlier conversation
// attached when there is one.
func (s *Service) choose(parent context.Context, shortlist negotiationclient.Shortlist, prior Conversation) (Selection, error) {
	if s.chooseAgent == nil {
		return Selection{}, fmt.Errorf("choosing agent is not built")
	}
	payload, err := jsonMarshal(choicePayload{Shortlist: shortlist, Earlier: earlierOrNil(prior)})
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
	return Selection{}, errNothingWorthBuying
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
