// Smoke test: the compiled shop graph must build with any tool set.
package shopgraph

import (
	"context"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiationclient"
)

func fakeGraphTools() Tools {
	return Tools{
		Search: func(context.Context, string, int64) ([]catalog.Product, error) { return nil, nil },
		Get: func(_ context.Context, id string) (catalog.Product, error) {
			return catalog.Product{ID: id, PricePaise: 100, Stock: 1}, nil
		},
		Offers: func(context.Context, string, int) (negotiationclient.Proposal, error) {
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

func TestClassifyOfferRoutesByMoney(t *testing.T) {
	w := Wallet{BalancePaise: 500000, SpendLimitPaise: 500000}
	offer := Offer{BasePaise: 240000, FinalPaise: 250000}
	if got := classifyOffer(offer, w); got != RouteAccept {
		t.Fatalf("in-band route = %s", got)
	}
	if got := classifyOffer(Offer{BasePaise: 240000, FinalPaise: 400000}, w); got != RouteNegotiate {
		t.Fatalf("premium-in-budget route = %s", got)
	}
	if got := classifyOffer(Offer{BasePaise: 240000, FinalPaise: 600000}, w); got != RouteDecline {
		t.Fatalf("over-ceiling route = %s", got)
	}
}
