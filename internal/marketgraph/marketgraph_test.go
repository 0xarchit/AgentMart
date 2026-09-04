// Tests for the merchant graph: bounded money, campaign layer, compilation.
package marketgraph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
)

func TestClampToRailsNeverSellsBelowFloor(t *testing.T) {
	// Strategy tried to undercut the floor.
	amount, note := clampToRails(60_000, 70_000, 50_000, 0, 100_000)
	if amount != 70_000 || note == "" {
		t.Fatalf("amount = %d, note = %q", amount, note)
	}
	// Strategy tried to undercut the buyer's own bid.
	amount, note = clampToRails(75_000, 70_000, 80_000, 0, 100_000)
	if amount != 80_000 || note == "" {
		t.Fatalf("buyer-bid floor: amount = %d, note = %q", amount, note)
	}
	// Strategy tried to exceed its own standing ask.
	amount, note = clampToRails(150_000, 70_000, 50_000, 0, 100_000)
	if amount != 100_000 || note == "" {
		t.Fatalf("ask ceiling: amount = %d, note = %q", amount, note)
	}
	// Amount already inside the rails passes through unannotated.
	amount, note = clampToRails(90_000, 70_000, 50_000, 0, 100_000)
	if amount != 90_000 || note != "" {
		t.Fatalf("in-rails: amount = %d, note = %q", amount, note)
	}
}

func TestTheConcessionFloorIsEnforcedRatherThanSuggested(t *testing.T) {
	// The schedule says hold at 90000 this round. A strategist conceding straight
	// to the buyer's bid used to be allowed through, because this floor only ever
	// reached the prompt and the audit payload.
	amount, note := clampToRails(72_000, 70_000, 50_000, 90_000, 100_000)
	if amount != 90_000 {
		t.Fatalf("amount = %d, want the round's concession floor held at 90000", amount)
	}
	if !strings.Contains(note, "concession floor") {
		t.Fatalf("note = %q, want it to name the bound that bit", note)
	}
	// The cost floor still wins when it is the higher of the two.
	if amount, _ = clampToRails(60_000, 95_000, 0, 90_000, 100_000); amount != 95_000 {
		t.Fatalf("amount = %d, want the cost floor at 95000", amount)
	}
	// A schedule above the standing ask must not let the merchant charge above it.
	amount, note = clampToRails(80_000, 70_000, 0, 150_000, 100_000)
	if amount != 100_000 {
		t.Fatalf("amount = %d, want the standing ask to cap every floor", amount)
	}
	if !strings.Contains(note, "standing ask") {
		t.Fatalf("note = %q, want the ask named as the bound", note)
	}
}

func TestNewWithoutModelStaysDeterministic(t *testing.T) {
	negotiator, err := New(Config{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if negotiator != nil {
		t.Fatal("expected nil negotiator so callers keep the concession schedule")
	}
}

func TestNewCompilesMerchantGraph(t *testing.T) {
	negotiator, err := New(Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("build merchant graph: %v", err)
	}
	if negotiator == nil || negotiator.graph == nil {
		t.Fatal("merchant graph missing")
	}
}

func TestNoProviderMeansNoFundedDiscount(t *testing.T) {
	negotiator := &Negotiator{}
	// A shelf holding forty units is not a discount budget. Without a campaign
	// source nothing funds a giveaway, whatever the stock level happens to be.
	for _, stock := range []int{40, 2} {
		tier, pct, notes := negotiator.eligibility(t.Context(), negotiation.CounterInput{
			Product: catalog.Product{Stock: stock},
		})
		if tier != "standard" || pct != 0 {
			t.Fatalf("stock %d gave tier %q pct %d", stock, tier, pct)
		}
		if len(notes) == 0 {
			t.Fatalf("stock %d gave no reason for having no budget", stock)
		}
	}
}

func TestFactsFromCarriesRailsAndTranscript(t *testing.T) {
	session, err := negotiation.New(negotiation.Proposal{ProductID: "p", Quantity: 1, BaseAmountPaise: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CounterOffer(negotiation.Counter{FinalAmountPaise: 120, Reason: "warranty"}); err != nil {
		t.Fatal(err)
	}
	partner := catalog.Product{Name: "CalmSkin Cream"}
	facts := factsFrom(negotiation.CounterInput{
		Session: session, Product: catalog.Product{Name: "TrimPro", Stock: 5, TrustScore: 92},
		Partner: &partner, FloorPaise: 70, AskPaise: 120, BuyerPaise: 90, MinAcceptablePaise: 100,
	})
	if facts.FloorPaise != 70 || facts.AskPaise != 120 || facts.MinAcceptablePaise != 100 {
		t.Fatalf("rails lost: %+v", facts)
	}
	if facts.BundleName != "CalmSkin Cream" || len(facts.Transcript) == 0 {
		t.Fatalf("bundle/transcript lost: %+v", facts)
	}
}

func TestAnOwnerWithNothingToPitchStillAnswers(t *testing.T) {
	event := &session.Event{Output: shopfrontAnswer{
		Greeting: "Nothing on the shelf suits that budget.",
		Options:  nil,
	}}
	answer, err := shopfrontAnswerFrom(event)
	if err != nil {
		t.Fatalf("a refusal in words is an answer: %v", err)
	}
	if answer.Greeting == "" || len(answer.Options) != 0 {
		t.Fatalf("answer = %+v, want the words and no options", answer)
	}
}

func TestAnUnreadableAnswerIsStillAFault(t *testing.T) {
	if _, err := shopfrontAnswerFrom(&session.Event{Output: "not an answer at all"}); err == nil {
		t.Fatal("expected a fault when nothing can be read")
	}
}

// stubTrading answers with fixed conditions or a refusal.
type stubTrading struct {
	conditions negotiation.TradingConditions
	err        error
}

// Conditions implements TradingProvider.
func (s stubTrading) Conditions(context.Context, string) (negotiation.TradingConditions, error) {
	return s.conditions, s.err
}

func TestTheShopsOwnTradingFiguresReachTheStrategist(t *testing.T) {
	negotiator := &Negotiator{trading: stubTrading{conditions: negotiation.TradingConditions{
		Observed: true, UnitsSold: 14, StockCoverDays: 3, RefundRatePct: 8, RefundRateKnown: true,
	}}}
	facts := Facts{}
	negotiator.addTradingConditions(t.Context(), &facts, "product")

	if !facts.TradingObserved || facts.UnitsSoldRecently != 14 || facts.StockCoverDays != 3 {
		t.Fatalf("facts = %+v", facts)
	}
	if !facts.RefundRateKnown || facts.RefundRatePct != 8 {
		t.Fatalf("refund figures = %+v", facts)
	}
}

func TestAnUnreadableTradingFigureIsNotedRatherThanZeroed(t *testing.T) {
	negotiator := &Negotiator{trading: stubTrading{err: errors.New("records are unreachable")}}
	facts := Facts{}
	negotiator.addTradingConditions(t.Context(), &facts, "product")

	// The strategist must be able to tell an absence from a zero, so the failure
	// is carried in the notes and nothing is presented as observed.
	if facts.TradingObserved || facts.RefundRateKnown {
		t.Fatalf("a failed read was presented as an observation: %+v", facts)
	}
	if len(facts.CampaignNotes) == 0 {
		t.Fatal("a failed read left no trace for the strategist or the trail")
	}
}

func TestNoTradingProviderLeavesEveryFigureUnobserved(t *testing.T) {
	negotiator := &Negotiator{}
	facts := Facts{}
	negotiator.addTradingConditions(t.Context(), &facts, "product")

	if facts.TradingObserved || facts.RefundRateKnown || facts.UnitsSoldRecently != 0 {
		t.Fatalf("facts = %+v", facts)
	}
}

// strategistProvider answers one strategy request with the choice given, in the
// wire shape the model layer expects. The graph is otherwise real, so this is the
// only way to run the price guard node at all: it sits behind the strategist, and
// the strategist needs a provider.
func strategistProvider(t *testing.T, choice StrategyChoice) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("strategist request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		function := "final_answer"
		if len(request.Tools) > 0 {
			function = request.Tools[0].Function.Name
		}
		arguments, err := json.Marshal(choice)
		if err != nil {
			t.Errorf("encode choice: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call-1", "type": "function",
					"function": map[string]any{"name": function, "arguments": string(arguments)},
				}},
			}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// recordedOffer is what the trail was handed for one priced offer.
type recordedOffer struct {
	facts    Facts
	decision Decision
}

type offerAuditor struct {
	seen []recordedOffer
	err  error
}

func (a *offerAuditor) RecordOfferDecision(_ context.Context, _ negotiation.CounterInput, facts Facts, decision Decision) error {
	a.seen = append(a.seen, recordedOffer{facts: facts, decision: decision})
	return a.err
}

// counterInput is one buyer counter to price, with rails the guard has to hold.
func counterInput(t *testing.T) negotiation.CounterInput {
	t.Helper()
	deal, err := negotiation.New(negotiation.Proposal{ProductID: "trim-9", Quantity: 1, BaseAmountPaise: 179_900})
	if err != nil {
		t.Fatal(err)
	}
	if err := deal.CounterOffer(negotiation.Counter{FinalAmountPaise: 181_339, Reason: "handling"}); err != nil {
		t.Fatal(err)
	}
	return negotiation.CounterInput{
		Session: deal, Product: catalog.Product{ID: "trim-9", Name: "TrimPro Nova", Stock: 20, TrustScore: 88},
		FloorPaise: 110_000, AskPaise: 181_339, BuyerPaise: 100_000, MinAcceptablePaise: 150_000,
		BuyerAccountID: "account-3",
	}
}

// TestThePriceGuardRunsInsideTheGraph executes the node that bounds the money.
// clampToRails is covered on its own, but nothing had ever run the graph, so the
// guard could have been handed the wrong rails, wired out of the edge list, or
// left reporting the model's number instead of the corrected one, and every test
// would still have passed.
func TestThePriceGuardRunsInsideTheGraph(t *testing.T) {
	// The model asks for less than the merchant's cost. The guard has to lift it,
	// and it has to say that it did.
	provider := strategistProvider(t, StrategyChoice{
		Strategy: StrategyConcede, AmountPaise: 90_000, Reason: "keen to close",
	})
	trail := &offerAuditor{}
	merchant, err := New(Config{Model: "stub", APIKey: "key", BaseURL: provider.URL}, nil, nil, trail)
	if err != nil {
		t.Fatal(err)
	}

	decision, err := merchant.Decide(t.Context(), counterInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if decision.AmountPaise != 150_000 {
		t.Fatalf("priced at %d, want the round's concession floor at 150000 held against a model asking 90000", decision.AmountPaise)
	}
	if decision.GuardNote == "" {
		t.Fatal("the price was corrected without saying so, which is a silent override of the model")
	}
	if decision.Strategy != "concede" || decision.Reason != "keen to close" {
		t.Fatalf("decision = %+v, want the model's own strategy and words kept", decision)
	}
	if decision.MarginPaise != 40_000 {
		t.Fatalf("margin = %d, want the corrected price less the cost floor", decision.MarginPaise)
	}

	if len(trail.seen) != 1 {
		t.Fatalf("wrote %d explanations, want exactly one", len(trail.seen))
	}
	wrote := trail.seen[0]
	if wrote.decision.AmountPaise != 150_000 || wrote.decision.GuardNote == "" {
		t.Fatalf("the trail records %+v, want the price actually offered and the correction", wrote.decision)
	}
	if wrote.facts.FloorPaise != 110_000 || wrote.facts.AskPaise != 181_339 || wrote.facts.MinAcceptablePaise != 150_000 {
		t.Fatalf("the trail records rails %+v, want the ones the guard held", wrote.facts)
	}
}

// TestAnOfferThatCannotBeExplainedIsNotSent is the fail closed claim the
// architecture makes, run rather than read. The guard writes the explanation
// before it returns, so a trail that refuses the write has to take the offer with
// it.
func TestAnOfferThatCannotBeExplainedIsNotSent(t *testing.T) {
	provider := strategistProvider(t, StrategyChoice{
		Strategy: StrategyHold, AmountPaise: 181_339, Reason: "the ask is fair",
	})
	trail := &offerAuditor{err: errors.New("audit_log is unreachable")}
	merchant, err := New(Config{Model: "stub", APIKey: "key", BaseURL: provider.URL}, nil, nil, trail)
	if err != nil {
		t.Fatal(err)
	}

	decision, err := merchant.Decide(t.Context(), counterInput(t))
	if err == nil {
		t.Fatalf("an unexplainable offer was returned anyway: %+v", decision)
	}
	if !strings.Contains(err.Error(), "audit_log is unreachable") {
		t.Fatalf("error = %v, want the trail's own cause carried out", err)
	}
	if decision.AmountPaise != 0 {
		t.Fatalf("a price came back with the failure: %+v", decision)
	}
	// And the same failure has to stop the buyer-facing path too, not only Decide.
	if _, cerr := merchant.Counter(t.Context(), counterInput(t)); cerr == nil {
		t.Fatal("Counter handed the buyer a price the shop could not explain")
	}
}

// stubCampaigns funds one fixed discount, standing in for the campaign rows the
// market binary reads.
type stubCampaigns struct{}

func (stubCampaigns) Eligibility(context.Context, negotiation.CounterInput) (string, int, []string, error) {
	return "gold", 15, []string{"second purchase this month"}, nil
}

// TestTheTrailRecordsTheFactsThePriceWasChosenOn covers the half of the audit row
// that was always empty. The campaign node assembles the loyalty tier, the funded
// percentage, its notes and the shop's own trading figures, and the strategist is
// shown all of it. The guard then rebuilt a bare set from the negotiation input,
// which carries none of those, so every offer_priced row in the trail claimed a
// standard tier, no funded discount, no notes and nothing observed, whatever the
// price had actually been reasoned from.
func TestTheTrailRecordsTheFactsThePriceWasChosenOn(t *testing.T) {
	provider := strategistProvider(t, StrategyChoice{
		Strategy: StrategyHold, AmountPaise: 181_339, Reason: "the ask is fair",
	})
	trail := &offerAuditor{}
	merchant, err := New(Config{Model: "stub", APIKey: "key", BaseURL: provider.URL},
		stubCampaigns{},
		stubTrading{conditions: negotiation.TradingConditions{
			Observed: true, UnitsSold: 14, StockCoverDays: 3, RefundRatePct: 8, RefundRateKnown: true,
		}},
		trail)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := merchant.Decide(t.Context(), counterInput(t)); err != nil {
		t.Fatal(err)
	}
	if len(trail.seen) != 1 {
		t.Fatalf("wrote %d explanations, want exactly one", len(trail.seen))
	}
	facts := trail.seen[0].facts
	if facts.LoyaltyTier != "gold" || facts.LoyaltyDiscountPct != 15 {
		t.Fatalf("trail records tier %q at %d%%, want the campaign the strategist was shown", facts.LoyaltyTier, facts.LoyaltyDiscountPct)
	}
	if len(facts.CampaignNotes) == 0 {
		t.Fatal("the trail carries no reason for the funded discount, which is what makes a price explainable")
	}
	if !facts.TradingObserved || facts.UnitsSoldRecently != 14 || facts.StockCoverDays != 3 {
		t.Fatalf("trail records trading facts %+v, want the figures the shop observed", facts)
	}
	if !facts.RefundRateKnown || facts.RefundRatePct != 8 {
		t.Fatalf("trail records refund confidence %+v, want what was measured", facts)
	}
	// The rails still have to be the ones the guard held, not a second reading.
	if facts.FloorPaise != 110_000 || facts.AskPaise != 181_339 {
		t.Fatalf("trail records rails %+v, want the ones the guard held", facts)
	}
}
