// Tests for the merchant remote-agent bridge.
package remotemerchant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewWithoutEndpointDegradesQuietly(t *testing.T) {
	merchant, err := New(t.Context(), Config{})
	if err != nil {
		t.Fatalf("empty endpoint should not error: %v", err)
	}
	if merchant != nil {
		t.Fatal("expected nil agent so callers keep their local tools")
	}
}

func TestNewResolvesCardWithSharedToken(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"name": "merchant-negotiation",
			"description": "Negotiates catalog offers.",
			"version": "v1.0.0",
			"defaultInputModes": ["application/json"],
			"defaultOutputModes": ["application/json"],
			"capabilities": {},
			"skills": [],
			"supportedInterfaces": [{"url": "` + "http://merchant.test/a2a/" + `", "transport": "JSONRPC"}]
		}`))
	}))
	defer server.Close()

	merchant, err := New(t.Context(), Config{Endpoint: server.URL + "/a2a/", SharedToken: "secret"})
	if err != nil {
		t.Fatalf("resolve merchant card: %v", err)
	}
	if merchant == nil {
		t.Fatal("expected a merchant agent")
	}
	if !strings.HasPrefix(sawAuth, "Bearer ") {
		t.Fatalf("card resolution did not carry the shared token: %q", sawAuth)
	}
	if merchant.Name() != "merchant_agent" {
		t.Fatalf("agent name = %q", merchant.Name())
	}
}

func TestNewFailsLoudlyOnUnreachableCard(t *testing.T) {
	if _, err := New(t.Context(), Config{Endpoint: "http://127.0.0.1:1/a2a/"}); err == nil {
		t.Fatal("expected an error for an unreachable agent card")
	}
}
