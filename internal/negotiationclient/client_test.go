// Tests for the merchant negotiation client.
package negotiationclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"agentmart/internal/catalog"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
)

func TestClientNegotiatesAgainstMerchantServer(t *testing.T) {
	server := negotiation.NewCatalogServer(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	client, err := New(httpServer.URL+"/negotiation", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := client.Propose(t.Context(), "product", 1)
	if err != nil || proposal.SessionID == "" || proposal.BaseAmountPaise != 100 {
		t.Fatalf("proposal = %+v, err = %v", proposal, err)
	}
	resolution, err := client.Accept(t.Context(), proposal.SessionID)
	if err != nil || resolution.Status != "accepted" || resolution.ProductID != "product" || resolution.Quantity != 1 {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
}

func TestAgentClientNegotiatesAgainstMerchantServer(t *testing.T) {
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	var err error
	merchantServer, err := negotiation.NewCatalogServerWithStore(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	}, negotiation.NewMemorySessionStore())
	if err != nil {
		t.Fatal(err)
	}
	handler, err = merchantagent.NewHandler(merchantServer, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAgentClient(t.Context(), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	proposal, err := client.Propose(t.Context(), "product", 1)
	if err != nil || proposal.SessionID == "" || proposal.BaseAmountPaise != 100 {
		t.Fatalf("proposal = %+v, err = %v", proposal, err)
	}
	resolution, err := client.Accept(t.Context(), proposal.SessionID)
	if err != nil || resolution.Status != "accepted" || resolution.ProductID != "product" || resolution.Quantity != 1 {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
}

func TestAFailedMerchantTaskIsNotReadAsAnEmptyAnswer(t *testing.T) {
	// A failed task carries its reason in the status. Falling back to the task
	// history would hand back our own request, which decodes as a valid answer
	// with nothing in it, so a broken shop would look like an empty shelf.
	request := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(`{"type":"browse","brief":"a trimmer"}`))
	failed := &a2a.Task{
		ID:      "task-1",
		History: []*a2a.Message{request},
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateFailed,
			Message: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("shop owner could not answer: provider returned 503")),
		},
	}

	text, err := extractAgentText(failed)
	if err == nil {
		t.Fatalf("a failed task must be an error, got text %q", text)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("the merchant's own reason must survive: %v", err)
	}
}
