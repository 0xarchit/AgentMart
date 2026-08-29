// Tests for the merchant negotiation client.
package negotiationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"agentmart/internal/catalog"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
	"agentmart/internal/runid"
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

func TestTheRunIdTravelsToTheShopOnEveryMessage(t *testing.T) {
	// The shop writes its own pricing explanations. Without the run on the wire
	// those rows land in the trail unattached to the purchase they caused.
	seen := make(chan string, 4)
	upstream := negotiation.NewCatalogServer(func(context.Context, string) (catalog.Product, error) {
		return catalog.Product{ID: "product", PricePaise: 100, Stock: 3, WarrantyYears: 2, TrustScore: 90}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var message struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		seen <- message.RunID
		r.Body = io.NopCloser(bytes.NewReader(body))
		upstream.Handler().ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	client, err := New(httpServer.URL+"/negotiation", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := runid.With(t.Context(), "run-1")
	proposal, err := client.Propose(ctx, "product", 1)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := client.Counter(ctx, proposal.SessionID, 90); err != nil {
		t.Fatalf("counter: %v", err)
	}
	for message := 1; message <= 2; message++ {
		if got := <-seen; got != "run-1" {
			t.Fatalf("message %d carried run %q, want run-1", message, got)
		}
	}

	if _, err := client.Propose(t.Context(), "product", 1); err != nil {
		t.Fatalf("propose outside a run: %v", err)
	}
	if got := <-seen; got != "" {
		t.Fatalf("work outside a run invented one: %q", got)
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
