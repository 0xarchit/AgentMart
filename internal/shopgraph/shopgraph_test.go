// Smoke test: the compiled shop graph must build with any tool set.
package shopgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/llmchat"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
)

func fakeGraphTools() Tools {
	return Tools{
		Browse: func(context.Context, string, int64, string) (negotiationclient.Shortlist, error) {
			return negotiationclient.Shortlist{
				Greeting: "welcome in",
				Options: []negotiationclient.ShortlistOption{{
					ProductID: "product", Name: "Trimmer", PricePaise: 100, Pitch: "solid everyday pick",
				}},
			}, nil
		},
		Get: func(_ context.Context, id string) (catalog.Product, error) {
			return catalog.Product{ID: id, PricePaise: 100, Stock: 1}, nil
		},
		Offers: func(_ context.Context, _ string, _ int, _ string) (negotiationclient.Proposal, error) {
			return negotiationclient.Proposal{}, nil
		},
		Counter: func(context.Context, string, int64) (negotiationclient.Resolution, error) {
			return negotiationclient.Resolution{}, nil
		},
		Accept: func(context.Context, string) (negotiationclient.Resolution, error) {
			return negotiationclient.Resolution{}, nil
		},
		Decline: func(context.Context, string, string) (negotiationclient.Resolution, error) {
			return negotiationclient.Resolution{}, nil
		},
	}
}

func TestNewCompilesShopGraph(t *testing.T) {
	service, err := New(t.Context(), Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, fakeGraphTools())
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	if service == nil || service.wfAgent == nil {
		t.Fatal("graph agent missing")
	}
}

// The buyer agent owns the accept/negotiate/ask_human/decline call. routeFor
// only carries that decision onto an edge, with one money guard.
func TestRouteForCarriesTheAgentsDecision(t *testing.T) {
	wallet := Wallet{BalancePaise: 500000, SpendLimitPaise: 500000}
	offer := Offer{BasePaise: 240000, FinalPaise: 250000}

	for decision, want := range map[string]string{
		"accept":    RouteAccept,
		"negotiate": RouteNegotiate,
		"ask_human": RouteAskHuman,
		"decline":   RouteDecline,
		"ACCEPT":    RouteAccept,
		"counter":   RouteNegotiate,
		"confirm":   RouteAskHuman,
		"reject":    RouteDecline,
	} {
		got, note := routeFor(Assessment{Decision: decision, Reason: "agent reasoning"}, offer, wallet)
		if got != want {
			t.Fatalf("decision %q routed to %s, want %s", decision, got, want)
		}
		if note != "" {
			t.Fatalf("decision %q should pass through unannotated, got %q", decision, note)
		}
	}
}

func TestRouteForEscalatesUnaffordableAccept(t *testing.T) {
	// An "accept" the wallet cannot fund goes to the human, not silently refused.
	got, note := routeFor(Assessment{Decision: "accept"},
		Offer{BasePaise: 240000, FinalPaise: 600000},
		Wallet{BalancePaise: 500000, SpendLimitPaise: 500000})
	if got != RouteAskHuman || note == "" {
		t.Fatalf("over-wallet accept: route = %s note = %q", got, note)
	}

	// Same for an accept above the stated budget.
	got, note = routeFor(Assessment{Decision: "accept"},
		Offer{BasePaise: 240000, FinalPaise: 300000},
		Wallet{BalancePaise: 900000, SpendLimitPaise: 900000, BudgetPaise: 250000})
	if got != RouteAskHuman || note == "" {
		t.Fatalf("over-budget accept: route = %s note = %q", got, note)
	}

	// Negotiate is never overridden by the money guard.
	got, _ = routeFor(Assessment{Decision: "negotiate"},
		Offer{BasePaise: 240000, FinalPaise: 900000},
		Wallet{BalancePaise: 100000, SpendLimitPaise: 100000})
	if got != RouteNegotiate {
		t.Fatalf("negotiate route = %s", got)
	}
}

func TestAFairBundleIsNotJudgedAsMarkupOnTheMainProduct(t *testing.T) {
	// A real quote: list 2400, warranty 300, trust handling 50, and a partner
	// cream at 399.20 included in the ask. Measured against the main product
	// alone the second product reads as markup and the run escalates for nothing.
	const (
		mainList = int64(240000)
		bundled  = int64(39920)
		ask      = int64(314920)
	)

	_, mainOnlyPct := premiumOver(ask, mainList)
	if mainOnlyPct <= AutoBuyPremiumMaxPct {
		t.Fatalf("the defect this guards is gone: main-only premium = %d%%", mainOnlyPct)
	}

	paise, pct := premiumOver(ask, mainList+bundled)
	if paise != 35000 {
		t.Fatalf("premium over everything included = %d, want 35000", paise)
	}
	if pct > AutoBuyPremiumMaxPct {
		t.Fatalf("a fair bundle must stay inside the band: %d%% over %d%%", pct, AutoBuyPremiumMaxPct)
	}

	if _, pct := premiumOver(ask, 0); pct != 0 {
		t.Fatalf("an unknown list value has no percentage to report, got %d", pct)
	}
}

func TestRouteForUnclearDecisionAsksHuman(t *testing.T) {
	got, note := routeFor(Assessment{Decision: "maybe?"},
		Offer{BasePaise: 240000, FinalPaise: 250000},
		Wallet{BalancePaise: 500000, SpendLimitPaise: 500000})
	if got != RouteAskHuman || note == "" {
		t.Fatalf("unclear decision: route = %s note = %q", got, note)
	}
}

func TestAnUnsettledNegotiationGoesToThePersonInsteadOfLosingTheRun(t *testing.T) {
	// The graph ended holding a quote that nothing settled. That is the person's
	// call, not an error, and it must not spend.
	service := &Service{}
	offer := Offer{
		SessionID: "session-1", ProductID: "product-1", ProductName: "Trimmer",
		Quantity: 1, BasePaise: 180000, FinalPaise: 233815, Reason: "extended warranty",
		ShopTurns: []negotiation.Turn{{Actor: "merchant", Message: "Welcome in"}},
	}

	result, err := service.resultFrom("run-1", nil, offer)
	if err != nil {
		t.Fatalf("an unsettled negotiation must not fail the run: %v", err)
	}
	if result.Action != ActionAskHuman || !result.NeedsApproval {
		t.Fatalf("action = %q needsApproval = %v, want the person asked", result.Action, result.NeedsApproval)
	}
	if result.FinalPaise != 233815 || result.SessionID != "session-1" {
		t.Fatalf("the quote must survive: %+v", result)
	}
	if len(result.Transcript) == 0 {
		t.Fatal("the person needs the conversation to judge the offer")
	}
	if _, err := service.resultFrom("run-1", nil, "not an outcome"); err == nil {
		t.Fatal("an unrecognised final output is still a failure")
	}
}

// TestNoMoneyMovingToolReachesAReasoningLayer keeps a deliberate safety
// decision honest: a tool calling layer that can create a charge can create the
// wrong charge, and no instruction fixes that. Charge creation stays in code,
// behind the gate, on values the gate has already validated.
func TestNoMoneyMovingToolReachesAReasoningLayer(t *testing.T) {
	service, err := New(t.Context(), Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, fakeGraphTools())
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	forbidden := []string{"charge", "capture", "payment_link", "paymentlink", "initiate_payment", "create_payment", "refund", "token", "mandate", "settle", "debit", "wallet"}
	for _, exposed := range service.negotiationTools() {
		name := strings.ToLower(exposed.Name())
		for _, word := range forbidden {
			if strings.Contains(name, word) {
				t.Fatalf("tool %q can move money and must not be reachable from a reasoning layer", exposed.Name())
			}
		}
	}
}

func TestAFundedDiscountIsNotTreatedAsAPremium(t *testing.T) {
	// A price below list is what a loyalty entitlement produces. It must read as a
	// negative premium, and the approval band must not fire on it: nobody needs to
	// be asked whether they mind paying less than the list price.
	const listPaise = 200_000
	const settled = 176_000

	paise, pct := premiumOver(settled, listPaise)
	if paise >= 0 {
		t.Fatalf("premium = %d, want a discount to read as negative", paise)
	}
	if pct >= 0 {
		t.Fatalf("premium pct = %d, want a discount to read as negative", pct)
	}

	// This is the band expression from the finalize step, pinned here so a change
	// to it cannot start escalating discounts unnoticed.
	bandCrossed := listPaise > 0 && paise > 0 && paise*100 > int64(listPaise)*AutoBuyPremiumMaxPct
	if bandCrossed {
		t.Fatal("a discount crossed the premium band")
	}
}

func TestEachRunReadsItsOwnMoneyFacts(t *testing.T) {
	// The chat loop and the public buyer surface share one service. One shared
	// slot let whichever caller started last decide what the other could spend,
	// and the wallet is what decides whether a run asks a person at all.
	service := &Service{}
	service.begin("run-person", Wallet{BalancePaise: 500000, SpendLimitPaise: 250000, AccountID: "account-1"}, nil, Conversation{})
	service.begin("run-agent", Wallet{BalancePaise: 99999999, SpendLimitPaise: 99999999}, nil, Conversation{})

	if got := service.walletFor("run-person").SpendLimitPaise; got != 250000 {
		t.Fatalf("the person's limit read as %d after another caller started", got)
	}
	if got := service.walletFor("run-agent").SpendLimitPaise; got != 99999999 {
		t.Fatalf("the outside caller's budget read as %d", got)
	}

	service.end("run-agent")
	if got := service.walletFor("run-person").SpendLimitPaise; got != 250000 {
		t.Fatalf("the person's limit read as %d after another caller finished", got)
	}
	// A finished run's facts are gone, and an unknown session can approve nothing.
	if got := service.walletFor("run-agent"); got.SpendLimitPaise != 0 || got.BalancePaise != 0 {
		t.Fatalf("a finished run still had money facts: %+v", got)
	}
}

func TestProgressGoesOnlyToTheRunBeingWatched(t *testing.T) {
	service := &Service{}
	var watched []string
	service.begin("run-watched", Wallet{}, func(line string) { watched = append(watched, line) }, Conversation{})
	service.begin("run-unwatched", Wallet{}, nil, Conversation{})

	service.noteTo("run-watched", "asking the shop")
	service.noteTo("run-unwatched", "this one has nobody listening")
	service.noteTo("run-never-started", "and this one does not exist")

	if len(watched) != 1 || watched[0] != "asking the shop" {
		t.Fatalf("progress = %v, want only this run's own line", watched)
	}
}

func TestConcurrentRunsDoNotShareAWallet(t *testing.T) {
	service := &Service{}
	const runs = 40
	var wg sync.WaitGroup
	failures := make(chan string, runs)

	for i := range runs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("run-%d", n)
			limit := int64(n+1) * 1000
			service.begin(id, Wallet{SpendLimitPaise: limit}, nil, Conversation{})
			defer service.end(id)
			// Read it back while every other run is storing its own.
			for range 20 {
				if got := service.walletFor(id).SpendLimitPaise; got != limit {
					failures <- fmt.Sprintf("run %d read a limit of %d, want %d", n, got, limit)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

func TestAFollowUpIsAskedAgainstWhatWasAlreadyShown(t *testing.T) {
	prior := Conversation{
		Brief: "a trimmer under 3000",
		Options: []PriorOption{
			{ProductID: "trim-nova", Name: "Nova 5 in 1", PricePaise: 179900},
			{ProductID: "trim-shield", Name: "Shield Pro", PricePaise: 240000},
		},
	}
	brief := briefWithHistory("the second one", prior)
	// The new words lead, because a refinement replaces the part of the request it
	// contradicts. Everything the reference could point at has to be present or
	// "the second one" means nothing.
	if !strings.HasPrefix(brief, "the second one") {
		t.Fatalf("the follow up does not lead: %s", brief)
	}
	for _, want := range []string{"a trimmer under 3000", "Nova 5 in 1", "Shield Pro", "1799.00", "2400.00"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(brief, want) {
				t.Fatalf("the brief lost %q: %s", want, brief)
			}
		})
	}
}

func TestAFirstMessageIsAskedExactlyAsItWasSaid(t *testing.T) {
	if brief := briefWithHistory("a trimmer under 3000", Conversation{}); brief != "a trimmer under 3000" {
		t.Fatalf("a first message was rewritten: %s", brief)
	}
	// An empty history is omitted rather than sent as an empty object, so there is
	// nothing for the agent to read meaning into.
	if earlierOrNil(Conversation{}) != nil {
		t.Fatal("an empty conversation was attached to the choice")
	}
	if earlierOrNil(Conversation{Brief: "a trimmer"}) == nil {
		t.Fatal("a real conversation was dropped from the choice")
	}
}

func TestRepeatingTheSameRequestDoesNotRepeatItBack(t *testing.T) {
	prior := Conversation{Brief: "a trimmer"}
	if brief := briefWithHistory("a trimmer", prior); brief != "a trimmer" {
		t.Fatalf("the same words were echoed back at the shop: %s", brief)
	}
}

func TestWhatTheShopShowedOutlivesTheRun(t *testing.T) {
	service := &Service{}
	service.begin("run-1", Wallet{}, nil, Conversation{})
	defer service.end("run-1")

	shown := []PriorOption{{ProductID: "trim-nova", Name: "Nova", PricePaise: 179900}}
	service.recordShown("run-1", shown)
	if got := service.shownFor("run-1"); len(got) != 1 || got[0].ProductID != "trim-nova" {
		t.Fatalf("shown = %+v", got)
	}
	// A run nobody started keeps nothing, rather than writing into another run.
	service.recordShown("run-absent", shown)
	if got := service.shownFor("run-absent"); got != nil {
		t.Fatalf("an unknown run reported %+v", got)
	}
}

// The choice comes back from a model as a product id, and everything after this
// point prices, judges and buys whatever id it named. This is what stands between
// an invented id and a settled order.
func TestAChoiceHasToBeSomethingTheShopShowed(t *testing.T) {
	shortlist := negotiationclient.Shortlist{
		Transcript: []negotiation.Turn{{Actor: "merchant", Message: "welcome in"}},
		Options: []negotiationclient.ShortlistOption{
			{ProductID: "trim-9", Name: "BladeMaster Pro", PricePaise: 349900},
		},
	}
	// What an earlier pass of the same conversation put on screen. A follow up
	// naming one of those is a real choice even though this pass did not show it.
	prior := Conversation{
		Brief:   "buy me a good trimmer",
		Options: []PriorOption{{ProductID: "trim-3", Name: "BladeMaster Lite", PricePaise: 129900}},
	}

	cases := []struct {
		name      string
		selection Selection
		wantNote  string
		wantErr   string
	}{
		{"shown this pass", Selection{ProductID: "trim-9", Quantity: 1, Rationale: "best warranty"},
			"Chose BladeMaster Pro: best warranty", ""},
		{"shown an earlier pass", Selection{ProductID: "trim-3", Quantity: 1, Rationale: "the cheaper one"},
			"Chose BladeMaster Lite: the cheaper one", ""},
		{"padded id", Selection{ProductID: "  trim-9  ", Quantity: 1, Rationale: "same product"},
			"Chose BladeMaster Pro: same product", ""},
		{"never shown", Selection{ProductID: "oil-1", Quantity: 1, Rationale: "invented"},
			"", `chose "oil-1", which the shop did not show`},
		{"nothing chosen", Selection{ProductID: "   ", Rationale: "nothing suited the ask"},
			"", "nothing on the shelf was worth buying: nothing suited the ask"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			service := &Service{}
			var notes []string
			service.begin("run-1", Wallet{}, func(line string) { notes = append(notes, line) }, prior)
			defer service.end("run-1")

			pick, err := service.pickFrom("run-1", shortlist, prior, one.selection)
			if one.wantErr != "" {
				if err == nil {
					t.Fatalf("the choice was accepted as %+v", pick)
				}
				if !strings.Contains(err.Error(), one.wantErr) {
					t.Fatalf("error = %v, want it to say %q", err, one.wantErr)
				}
				if len(notes) != 0 {
					t.Fatalf("a refused choice was reported as progress: %v", notes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// The id that travels on has to be the one the shop can look up.
			if pick.ProductID != strings.TrimSpace(one.selection.ProductID) {
				t.Fatalf("product id = %q, want %q", pick.ProductID, strings.TrimSpace(one.selection.ProductID))
			}
			// The opening turns travel with the choice, so the person can be shown
			// what was actually said in the shop.
			if len(pick.ShopTranscript) != 1 || pick.ShopTranscript[0].Message != "welcome in" {
				t.Fatalf("the shop's own words were dropped: %+v", pick.ShopTranscript)
			}
			// The note names the product rather than the id, because a person reads it.
			if len(notes) != 1 || notes[0] != one.wantNote {
				t.Fatalf("progress = %v, want %q", notes, one.wantNote)
			}
		})
	}
}

// A model that names a product but no count means one of it, not none of it.
// TestRefusingEverythingOnTheShelfIsAJudgementNotALostRun pins the difference
// between the two ways a buyer can end up with nothing. Both are the agent
// refusing the shelf, and both must decline: a plain error here scored the more
// deliberate of the two, the one where the agent explained itself, as a failed
// run, so the same refusal read as a decline or a fault depending only on whether
// the model bothered to give a reason.
func TestRefusingEverythingOnTheShelfIsAJudgementNotALostRun(t *testing.T) {
	service := &Service{}
	shortlist := negotiationclient.Shortlist{
		Options: []negotiationclient.ShortlistOption{{ProductID: "cream-1", Name: "CalmSkin", PricePaise: 49900}},
	}
	_, err := service.pickFrom("run-1", shortlist, Conversation{},
		Selection{Rationale: "the only thing on offer is a cream, not a trimmer"})
	if err == nil {
		t.Fatal("an empty choice was accepted")
	}
	if !errors.Is(err, errNothingWorthBuying) {
		t.Fatalf("error = %v, want the run loop to recognise it and decline", err)
	}
	// The agent's own words have to survive, because that reason is what the person
	// is shown and what the trail records.
	if !strings.Contains(err.Error(), "not a trimmer") {
		t.Fatalf("error = %v, want it to carry the agent's reason", err)
	}
}

// TestAnActionNobodyDefinedGoesToThePersonNotToACharge closes the last gap on the
// one node a model writes. The buyer binary sends a decline back as a message and
// an ask to a person, and treats everything else as a purchase, so any word the
// negotiating agent invented used to spend money it never asked to spend.
func TestAnActionNobodyDefinedGoesToThePersonNotToACharge(t *testing.T) {
	service := &Service{}
	service.begin("run-1", Wallet{}, nil, Conversation{})
	defer service.end("run-1")
	service.recordPriced("run-1", pricedGoods{productID: "trim-9", quantity: 1})

	for _, invented := range []string{"hold", "counter", "negotiate", "wait", "BUY"} {
		result, err := service.resultFrom("run-1", nil,
			Outcome{Action: invented, FinalPaise: 181339, SessionID: "session-1"})
		if err != nil {
			t.Fatalf("%q: %v", invented, err)
		}
		if result.Action != ActionAskHuman || !result.NeedsApproval {
			t.Fatalf("%q became action %q needsApproval=%v, want the person asked", invented, result.Action, result.NeedsApproval)
		}
		if !strings.Contains(result.Rationale, invented) {
			t.Fatalf("%q: the person is not told what the agent actually said: %q", invented, result.Rationale)
		}
	}
	// The three words that are defined still mean what they say.
	for _, defined := range []Action{ActionBuy, ActionAskHuman, ActionDecline} {
		result, err := service.resultFrom("run-1", nil,
			Outcome{Action: string(defined), FinalPaise: 181339, SessionID: "session-1"})
		if err != nil {
			t.Fatalf("%q: %v", defined, err)
		}
		if result.Action != defined {
			t.Fatalf("action %q was rewritten to %q", defined, result.Action)
		}
	}
}

// TestASettledPriceIsTheOneTheShopAgreedTo finishes the trust boundary on the
// negotiate route. The agent reads the merchant's number out of a tool result and
// then retypes it into its own answer, so without holding the settlement to what
// the shop confirmed, the amount charged is the agent's account of the price.
func TestASettledPriceIsTheOneTheShopAgreedTo(t *testing.T) {
	service := &Service{}
	service.begin("run-1", Wallet{}, nil, Conversation{})
	defer service.end("run-1")
	service.recordPriced("run-1", pricedGoods{productID: "trim-9", quantity: 1})

	// Only an accepted answer settles anything. A counter and a decline leave the
	// deal open, so neither may become the price.
	service.recordMerchantAnswer("run-1", string(negotiation.StatusCountered), 150000)
	service.recordMerchantAnswer("run-1", string(negotiation.StatusDeclined), 120000)
	if got := service.settledFor("run-1"); got != 0 {
		t.Fatalf("an unsettled answer became the price: %d", got)
	}

	service.recordMerchantAnswer("run-1", string(negotiation.StatusAccepted), 181339)
	result, err := service.resultFrom("run-1", nil,
		Outcome{Action: string(ActionBuy), FinalPaise: 99999, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalPaise != 181339 {
		t.Fatalf("charged %d, want the amount the shop agreed to", result.FinalPaise)
	}

	// With no merchant answer on this pass the outcome's own amount stands: the
	// accept, decline and ask routes never call the agent's tools, and each already
	// takes its amount from the merchant's resolution.
	quiet := &Service{}
	quiet.begin("run-2", Wallet{}, nil, Conversation{})
	defer quiet.end("run-2")
	untouched, err := quiet.resultFrom("run-2", nil,
		Outcome{Action: string(ActionBuy), FinalPaise: 181339, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if untouched.FinalPaise != 181339 {
		t.Fatalf("amount = %d, want the merchant-sourced outcome to survive", untouched.FinalPaise)
	}
}

func TestAChoiceWithNoCountMeansOne(t *testing.T) {
	service := &Service{}
	shortlist := negotiationclient.Shortlist{
		Options: []negotiationclient.ShortlistOption{{ProductID: "trim-9", Name: "BladeMaster Pro"}},
	}
	pick, err := service.pickFrom("run-1", shortlist, Conversation{}, Selection{ProductID: "trim-9"})
	if err != nil {
		t.Fatal(err)
	}
	if pick.Quantity != 1 {
		t.Fatalf("quantity = %d, want one", pick.Quantity)
	}
}

// TestTheNegotiatingAgentCannotChangeWhatIsBeingBought pins the trust boundary on
// the one node whose output a model writes. The agent settles the price of a
// basket it was handed; the basket itself must not be in its reach, or a real but
// different product, a larger count, or an invented attached amount would settle
// and gate cleanly while charging for something nobody chose. The whole fence
// rests on schema derivation honouring json:"-", so this asserts that too.
func TestTheNegotiatingAgentCannotChangeWhatIsBeingBought(t *testing.T) {
	schema, err := llmchat.SchemaFor[Outcome]()
	if err != nil {
		t.Fatalf("derive the settled outcome schema: %v", err)
	}
	for _, field := range []string{"product_id", "quantity", "bundled_paise", "quoted_at"} {
		if _, reachable := schema.Properties[field]; reachable {
			t.Fatalf("%q is in the negotiating agent's output schema, so it can change what is bought", field)
		}
	}
	// The fence is worthless if it silently removed everything: the agent still has
	// to be able to report what it settled at and why.
	for _, field := range []string{"action", "final_amount_paise", "rationale"} {
		if _, reachable := schema.Properties[field]; !reachable {
			t.Fatalf("%q left the output schema, so the agent can no longer report what it did", field)
		}
	}
}

// TestASettledOutcomeDescribesTheBasketTheShopPriced is the other half of that
// fence: settlement reads the basket back from the run rather than from the
// outcome, so even an outcome that names different goods cannot redirect the
// money.
func TestASettledOutcomeDescribesTheBasketTheShopPriced(t *testing.T) {
	service := &Service{}
	service.begin("run-1", Wallet{}, nil, Conversation{})
	defer service.end("run-1")
	service.recordPriced("run-1", pricedGoods{productID: "trim-9", quantity: 2, bundledPaise: 39920})

	substituted := Outcome{
		Action: string(ActionBuy), ProductID: "oil-1", Quantity: 9,
		BundledPaise: 500000, FinalPaise: 388013, SessionID: "session-1",
	}
	result, err := service.resultFrom("run-1", nil, substituted)
	if err != nil {
		t.Fatalf("a settled outcome must still settle: %v", err)
	}
	if result.ProductID != "trim-9" {
		t.Fatalf("product = %q, want the product the shop priced", result.ProductID)
	}
	if result.Quantity != 2 {
		t.Fatalf("quantity = %d, want the count the shop priced", result.Quantity)
	}
	if result.FinalPaise != 388013 {
		t.Fatalf("amount = %d, want the negotiated amount, which is the agent's to report", result.FinalPaise)
	}

	// A caller that never began a run has nothing to hold the outcome to, and must
	// not be silently handed an empty basket instead.
	loose := &Service{}
	fallback, err := loose.resultFrom("run-2", nil, substituted)
	if err != nil {
		t.Fatalf("resultFrom without a run: %v", err)
	}
	if fallback.ProductID != "oil-1" || fallback.Quantity != 9 {
		t.Fatalf("without a priced basket the outcome is all there is: %+v", fallback)
	}
}

// TestANegotiatedOutcomeStillCarriesWhenTheShopQuoted covers the one route where
// the quote time could not travel on the Outcome. Accept, escalate and decline all
// build their Outcome in Go and copy the offer's time onto it. The negotiating
// stage's Outcome is written by a model, and QuotedAt is fenced out of that shape
// so a model can never claim a price is fresher than it is, which also means the
// field comes back as the zero time. Left there, the gate has nothing to age a
// negotiated price against and every stale price passes.
func TestANegotiatedOutcomeStillCarriesWhenTheShopQuoted(t *testing.T) {
	service, err := New(t.Context(), Config{Model: "stub", APIKey: "key", BaseURL: "http://localhost:0"}, fakeGraphTools())
	if err != nil {
		t.Fatal(err)
	}
	const session = "run-quote"
	quoted := time.Now().UTC().Add(-90 * time.Second)
	service.begin(session, Wallet{BalancePaise: 500000, SpendLimitPaise: 500000}, nil, Conversation{})
	service.recordPriced(session, pricedGoods{productID: "trim-9", quantity: 1, quotedAt: quoted})

	// What the negotiating agent hands back: no quote time, because it cannot write
	// one, and no product or count either for the same reason.
	result, err := service.resultFrom(session, nil, Outcome{
		Action: string(ActionBuy), ProductName: "Trimmer", FinalPaise: 181339,
		Rationale: "settled at the shop's number", SessionID: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.QuotedAt.Equal(quoted) {
		t.Fatalf("quoted at %v, want the %v the run recorded when the shop priced this basket: a zero time leaves the gate unable to refuse a stale price", result.QuotedAt, quoted)
	}

	// A stage that writes its own time keeps it, so this is a fallback and not an
	// override of the merchant's own statement.
	own := time.Now().UTC().Add(-5 * time.Second)
	settled, err := service.resultFrom(session, nil, Outcome{
		Action: string(ActionBuy), ProductName: "Trimmer", FinalPaise: 181339,
		Rationale: "accepted", SessionID: session, QuotedAt: own,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settled.QuotedAt.Equal(own) {
		t.Fatalf("quoted at %v, want the stage's own %v", settled.QuotedAt, own)
	}
}
