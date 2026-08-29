// Smoke test: the compiled shop graph must build with any tool set.
package shopgraph

import (
	"context"
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

	result, err := service.resultFrom(nil, offer)
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
	if _, err := service.resultFrom(nil, "not an outcome"); err == nil {
		t.Fatal("an unrecognised final output is still a failure")
	}
}
