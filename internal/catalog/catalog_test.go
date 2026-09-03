// Tests for catalog validation and response mapping.
package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/supabase"
)

// TestTheMerchantPrivateCostSurvivesTheDecode is the test whose absence let the
// cost floor read zero for every product in production. Product.CostPaise is
// tagged json:"-" so the cost can never leak through a buyer-facing payload, and
// the same tag made encoding/json drop it on the way back from the database.
// Every other test builds products as Go literals, so none of them crossed a
// decode and none of them noticed.
func TestTheMerchantPrivateCostSurvivesTheDecode(t *testing.T) {
	var asked string
	shop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("select")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"trim-9","name":"BladeMaster Pro","category":"trimmer",` +
			`"price_paise":179900,"stock":20,"warranty_years":2,"trust_score":88,` +
			`"combo_with":null,"combo_discount_pct":0,"cost_paise":110000}]`))
	}))
	defer shop.Close()

	db, err := supabase.NewClient(shop.URL, "test-key", shop.Client())
	if err != nil {
		t.Fatal(err)
	}
	product, err := NewService(db).GetWithCost(t.Context(), "trim-9")
	if err != nil {
		t.Fatal(err)
	}
	if product.CostPaise != 110000 {
		t.Fatalf("cost = %d, want the column that was selected. A zero cost means the floor cannot stop a loss.", product.CostPaise)
	}
	if product.PricePaise != 179900 || product.ID != "trim-9" {
		t.Fatalf("the rest of the product was mangled: %+v", product)
	}
	if !strings.Contains(asked, "cost_paise") {
		t.Fatalf("select = %q, want it to ask for the cost", asked)
	}

	// The cost must still be unable to leave: buyer-facing payloads serialize this
	// same type, so a fix that made the cost decode by exposing the field would
	// hand the merchant's margin to the buyer.
	encoded, err := json.Marshal(product)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cost") || strings.Contains(string(encoded), "110000") {
		t.Fatalf("the private cost is serialized to callers: %s", encoded)
	}
}

func TestCheckStockRejectsNonPositiveQuantity(t *testing.T) {
	service := &Service{}
	_, err := service.CheckStock(t.Context(), "product", 0)
	if err == nil {
		t.Fatal("expected quantity validation error")
	}
}

func TestEscapeFilter(t *testing.T) {
	if got := escapeFilter("a*b,c"); got != `a\*b\,c` {
		t.Fatalf("escapeFilter() = %q", got)
	}
}
