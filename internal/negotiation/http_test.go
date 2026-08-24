// Tests for the negotiation JSON contract.
package negotiation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/catalog"
)

func TestNegotiationUsesCatalogReader(t *testing.T) {
	calls := 0
	handler := NewCatalogServer(func(_ context.Context, id string) (catalog.Product, error) {
		calls++
		return catalog.Product{ID: id, PricePaise: 100, Stock: 10}, nil
	}).Handler()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"propose","product_id":"product","qty":1}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, catalog calls = %d", response.Code, calls)
	}
}

func TestNegotiationHTTPFlow(t *testing.T) {
	handler := NewServer([]catalog.Product{{ID: "product", PricePaise: 100, Stock: 10, WarrantyYears: 1}}).Handler()

	propose := httptest.NewRecorder()
	proposeRequest := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"propose","product_id":"product","qty":1}`))
	handler.ServeHTTP(propose, proposeRequest)
	if propose.Code != http.StatusOK {
		t.Fatalf("propose status = %d", propose.Code)
	}

	var counter struct {
		SessionID        string `json:"session_id"`
		BaseAmountPaise  int64  `json:"base_amount_paise"`
		FinalAmountPaise int64  `json:"final_amount_paise"`
		WarrantyYears    int    `json:"warranty_years"`
		TrustScore       int    `json:"trust_score"`
	}
	if err := json.NewDecoder(propose.Body).Decode(&counter); err != nil {
		t.Fatalf("decode counter: %v", err)
	}
	if counter.SessionID == "" || counter.BaseAmountPaise != 100 || counter.FinalAmountPaise != 10100 || counter.WarrantyYears != 1 {
		t.Fatalf("unexpected counter: %+v", counter)
	}

	accept := httptest.NewRecorder()
	acceptRequest := httptest.NewRequest(http.MethodPost, "/negotiation", bytes.NewBufferString(`{"type":"accept","session_id":"`+counter.SessionID+`"}`))
	handler.ServeHTTP(accept, acceptRequest)
	if accept.Code != http.StatusOK {
		t.Fatalf("accept status = %d", accept.Code)
	}

	var result struct {
		Status           Status `json:"status"`
		BaseAmountPaise  int64  `json:"base_amount_paise"`
		FinalAmountPaise int64  `json:"final_amount_paise"`
		UpliftPaise      int64  `json:"uplift_paise"`
	}
	if err := json.NewDecoder(accept.Body).Decode(&result); err != nil {
		t.Fatalf("decode accepted session: %v", err)
	}
	if result.Status != StatusAccepted || result.BaseAmountPaise != 100 || result.FinalAmountPaise != 10100 || result.UpliftPaise != 10000 {
		t.Fatalf("unexpected accepted session: %+v", result)
	}
}
