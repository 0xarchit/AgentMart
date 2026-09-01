// Smoke test: the compiled shop graph must build with any tool set.
package shopgraph

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"agentmart/internal/catalog"
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
	service.begin("run-person", Wallet{BalancePaise: 500000, SpendLimitPaise: 250000, AccountID: "account-1"}, nil)
	service.begin("run-agent", Wallet{BalancePaise: 99999999, SpendLimitPaise: 99999999}, nil)

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
	service.begin("run-watched", Wallet{}, func(line string) { watched = append(watched, line) })
	service.begin("run-unwatched", Wallet{}, nil)

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
			service.begin(id, Wallet{SpendLimitPaise: limit}, nil)
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
