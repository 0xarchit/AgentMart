// Tests for the shop's opening turn: the merchant shows what it has, and no
// displayed number is allowed to come from the model.
package negotiation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"agentmart/internal/catalog"
)

type fakeShopfront struct {
	saw     BrowseInput
	answer  BrowseOutput
	failing bool
}

func (f *fakeShopfront) Shortlist(_ context.Context, input BrowseInput) (BrowseOutput, error) {
	f.saw = input
	if f.failing {
		return BrowseOutput{}, fmt.Errorf("provider unavailable")
	}
	return f.answer, nil
}

func trimmerStock() []catalog.Product {
	partner := "oil"
	return []catalog.Product{
		{ID: "titan", Name: "TrimPro Titan XL", Category: "trimmer", PricePaise: 269900, Stock: 8, WarrantyYears: 4, TrustScore: 94, ComboWith: &partner, ComboDiscountPct: 20},
		{ID: "lite", Name: "TrimPro Lite", Category: "trimmer", PricePaise: 129900, Stock: 45, WarrantyYears: 1, TrustScore: 74},
	}
}

func browseServer(t *testing.T, shopfront Shopfront) *Server {
	t.Helper()
	server, err := NewCatalogServerWithStore(func(_ context.Context, id string) (catalog.Product, error) {
		for _, product := range trimmerStock() {
			if product.ID == id {
				return product, nil
			}
		}
		return catalog.Product{}, fmt.Errorf("not found")
	}, NewMemorySessionStore())
	if err != nil {
		t.Fatal(err)
	}
	return server.WithShopfront(shopfront, func(context.Context, catalog.SearchRequest) ([]catalog.Product, error) {
		return trimmerStock(), nil
	})
}

func TestBrowseReturnsThePitchAndTheOpeningTurns(t *testing.T) {
	shopfront := &fakeShopfront{answer: BrowseOutput{
		Greeting: "welcome in, plenty of trimmers today",
		Options: []BrowseOption{
			{ProductID: "titan", Pitch: "our best build, four year warranty", Includes: "beard oil at 20 percent off"},
			{ProductID: "lite", Pitch: "does the job for daily upkeep"},
		},
		Closing: "say the word and I will price one up",
	}}
	server := browseServer(t, shopfront)

	response, err := server.browse(context.Background(), negotiationRequest{
		Type: "browse", Brief: "a good trimmer", BudgetPaise: 300000, AccountID: "account-1",
	})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}

	if shopfront.saw.Brief != "a good trimmer" || shopfront.saw.BudgetPaise != 300000 {
		t.Fatalf("shop owner saw %+v", shopfront.saw)
	}
	if len(shopfront.saw.Candidates) != 2 {
		t.Fatalf("shop owner was shown %d candidates, want 2", len(shopfront.saw.Candidates))
	}

	options, ok := response["options"].([]BrowseOption)
	if !ok || len(options) != 2 {
		t.Fatalf("options = %+v", response["options"])
	}
	// Prices and names are the catalog's, not the model's.
	if options[0].Name != "TrimPro Titan XL" || options[0].PricePaise != 269900 {
		t.Fatalf("first option = %+v, want catalog name and price", options[0])
	}
	if options[0].WarrantyYears != 4 || options[0].Stock != 8 {
		t.Fatalf("first option lost its catalog facts: %+v", options[0])
	}
	if options[0].Pitch == "" {
		t.Fatal("the pitch should survive: that part is the shop owner's")
	}

	turns, ok := response["transcript"].([]Turn)
	if !ok || len(turns) != 2 {
		t.Fatalf("transcript = %+v, want the buyer's ask and the shop's answer", response["transcript"])
	}
	if turns[0].Actor != "buyer" || !strings.Contains(turns[0].Message, "a good trimmer") {
		t.Fatalf("opening turn = %+v", turns[0])
	}
	if turns[1].Actor != "merchant" || !strings.Contains(turns[1].Message, "TrimPro Titan XL") {
		t.Fatalf("shop turn = %+v", turns[1])
	}
	// The transcript quotes real money, so it must match the catalog.
	if !strings.Contains(turns[1].Message, "2699.00") {
		t.Fatalf("shop turn should quote the catalog price: %s", turns[1].Message)
	}
}

func TestBrowseDropsProductsTheShopDoesNotHave(t *testing.T) {
	shopfront := &fakeShopfront{answer: BrowseOutput{
		Options: []BrowseOption{
			{ProductID: "philips-bt3231", Pitch: "invented from world knowledge", PricePaise: 199900},
			{ProductID: "titan", Pitch: "this one is real"},
			{ProductID: "titan", Pitch: "and named twice"},
		},
	}}
	server := browseServer(t, shopfront)

	response, err := server.browse(context.Background(), negotiationRequest{Type: "browse", Brief: "trimmer"})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	options := response["options"].([]BrowseOption)
	if len(options) != 1 || options[0].ProductID != "titan" {
		t.Fatalf("options = %+v, want only the real product once", options)
	}
	if options[0].PricePaise != 269900 {
		t.Fatalf("price = %d, want the catalog price", options[0].PricePaise)
	}
}

func TestBrowseRefusesWhenNothingRealIsNamed(t *testing.T) {
	shopfront := &fakeShopfront{answer: BrowseOutput{
		Options: []BrowseOption{{ProductID: "does-not-exist", Pitch: "hallucinated"}},
	}}
	server := browseServer(t, shopfront)

	if _, err := server.browse(context.Background(), negotiationRequest{Type: "browse", Brief: "trimmer"}); err == nil {
		t.Fatal("expected a refusal when no named product is in stock")
	}
}

func TestBrowseSurfacesProviderFailure(t *testing.T) {
	server := browseServer(t, &fakeShopfront{failing: true})

	_, err := server.browse(context.Background(), negotiationRequest{Type: "browse", Brief: "trimmer"})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("err = %v, want the real provider failure, not a scripted shortlist", err)
	}
}

func TestBrowseNeedsABrief(t *testing.T) {
	server := browseServer(t, &fakeShopfront{})

	if _, err := server.browse(context.Background(), negotiationRequest{Type: "browse", Brief: "  "}); err == nil {
		t.Fatal("expected a refusal without a brief")
	}
}

func TestBrowseAsksStockWithoutTreatingTheBriefAsASearchTerm(t *testing.T) {
	// A person says "buy me one trimmer". No product is named that, so passing the
	// sentence to the catalog as a search term finds nothing and the shop appears
	// empty. The owner must be shown their stock and left to read the brief.
	var asked catalog.SearchRequest
	server, err := NewCatalogServerWithStore(func(_ context.Context, id string) (catalog.Product, error) {
		return catalog.Product{}, fmt.Errorf("not needed: %s", id)
	}, NewMemorySessionStore())
	if err != nil {
		t.Fatal(err)
	}
	server = server.WithShopfront(&fakeShopfront{answer: BrowseOutput{
		Greeting: "come in",
		Options:  []BrowseOption{{ProductID: "titan", Pitch: "our best build"}},
		Closing:  "say the word",
	}},
		func(_ context.Context, request catalog.SearchRequest) ([]catalog.Product, error) {
			asked = request
			if strings.TrimSpace(request.Query) != "" {
				return nil, nil
			}
			return trimmerStock(), nil
		})

	out, err := server.browse(context.Background(), negotiationRequest{
		Type: "browse", Brief: "buy me one trimmer", BudgetPaise: 250000,
	})
	if err != nil {
		t.Fatalf("a plain sentence must still reach the shop: %v", err)
	}
	if asked.Query != "" {
		t.Fatalf("the catalog was queried for %q instead of being listed", asked.Query)
	}
	if asked.MaxPricePaise != 250000 {
		t.Fatalf("stock must stay inside the budget, got %d", asked.MaxPricePaise)
	}
	if options, ok := out["options"].([]BrowseOption); !ok || len(options) == 0 {
		t.Fatalf("expected a pitched shortlist, got %v", out["options"])
	}
}
