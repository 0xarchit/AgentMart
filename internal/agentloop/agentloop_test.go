// Tests for the bounded buyer agent loop (deterministic path) and band rule.
package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"agentmart/internal/negotiationclient"
	buyerreasoning "agentmart/internal/reasoning"
)

type fakeTools struct {
	search     []catalog.Product
	getByID    map[string]catalog.Product
	offers     negotiationclient.Proposal
	countered  []int64
	failOffers bool
}

func (f *fakeTools) tools() Tools {
	return Tools{
		Search: func(context.Context, string, int64) ([]catalog.Product, error) { return f.search, nil },
		Get: func(_ context.Context, id string) (catalog.Product, error) {
			if product, ok := f.getByID[id]; ok {
				return product, nil
			}
			return catalog.Product{}, errFake
		},
		Offers: func(context.Context, string, int) (negotiationclient.Proposal, error) {
			if f.failOffers {
				return negotiationclient.Proposal{}, errFake
			}
			return f.offers, nil
		},
		Counter: func(_ context.Context, _ string, paise int64) (negotiationclient.Resolution, error) {
			f.countered = append(f.countered, paise)
			// Merchant accepts any counter at or above 80% of the ask.
			minimum := f.offers.FinalAmountPaise * 80 / 100
			status := "countered"
			final := minimum
			if paise >= minimum {
				status = "accepted"
				final = paise
			}
			return negotiationclient.Resolution{
				SessionID: f.offers.SessionID, Status: status, ProductID: f.offers.ProductID,
				FinalAmountPaise: final,
				Transcript:       []negotiation.Turn{{Actor: "merchant", Message: "deal"}},
			}, nil
		},
	}
}

var errFake = errors.New("fake failure")

type errorString string

func (e errorString) Error() string { return string(e) }

func wallet(balance int64) WalletFacts {
	return WalletFacts{BalancePaise: balance, SpendLimitPaise: balance}
}

const trimmerID = "trimmer-1"

func fixtureTools() *fakeTools {
	return &fakeTools{
		search: []catalog.Product{{ID: trimmerID, Name: "TrimPro Shield", PricePaise: 240000, Stock: 5}},
		getByID: map[string]catalog.Product{
			trimmerID: {ID: trimmerID, Name: "TrimPro Shield", PricePaise: 240000, Stock: 5},
		},
		offers: negotiationclient.Proposal{
			SessionID: "sess-1", ProductID: trimmerID, Quantity: 1,
			BaseAmountPaise: 240000, FinalAmountPaise: 250000, Reason: "extended warranty",
		},
	}
}

func TestDeterministicBuysWithinBand(t *testing.T) {
	fakes := fixtureTools()
	service, err := New(t.Context(), buyerreasoning.Config{}, fakes.tools())
	if err != nil {
		t.Fatal(err)
	}
	result := service.Run(t.Context(), "buy me a trimmer", wallet(300000))
	if result.Action != ActionBuy {
		t.Fatalf("action = %s rationale = %s steps = %v", result.Action, result.Rationale, result.Steps)
	}
	if result.FinalPaise != 250000 || result.Product.ID != trimmerID {
		t.Fatalf("result = %+v", result)
	}
}

func TestDeterministicDeclinesOverWallet(t *testing.T) {
	fakes := fixtureTools()
	service, _ := New(t.Context(), buyerreasoning.Config{}, fakes.tools())
	result := service.Run(t.Context(), "buy me a trimmer", wallet(100000))
	if result.Action != ActionDecline {
		t.Fatalf("action = %s (%s)", result.Action, result.Rationale)
	}
}

func TestDeterministicAsksHumanOverPremiumBand(t *testing.T) {
	fakes := fixtureTools()
	fakes.offers.FinalAmountPaise = 400000 // 400000 vs base 240000 = 66% premium
	fakes.getByID[trimmerID] = catalog.Product{ID: trimmerID, Name: "TrimPro Shield", PricePaise: 240000, Stock: 5}
	service, _ := New(t.Context(), buyerreasoning.Config{}, fakes.tools())
	result := service.Run(t.Context(), "buy me a trimmer", wallet(500000))
	if result.Action != ActionAskHuman || !result.NeedsApproval {
		t.Fatalf("action = %s needsApproval = %v (%s)", result.Action, result.NeedsApproval, result.Rationale)
	}
	if len(result.Transcript) == 0 {
		t.Fatalf("expected counter transcript")
	}
}

func TestDeterministicDeclinesWithoutMatch(t *testing.T) {
	fakes := fixtureTools()
	fakes.search = nil
	service, _ := New(t.Context(), buyerreasoning.Config{}, fakes.tools())
	result := service.Run(t.Context(), "buy me a spaceship", wallet(999999))
	if result.Action != ActionDecline || !strings.Contains(result.Rationale, "no matching") {
		t.Fatalf("result = %+v", result)
	}
}
