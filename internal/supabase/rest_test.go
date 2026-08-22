// Tests for the trusted Supabase REST transport.
package supabase

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRPCSendsTrustedHeadersAndPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/rpc/example" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing trusted authorization header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"value":7}` {
			t.Fatalf("unexpected body: %s", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RPC(context.Background(), "example", struct {
		Value int `json:"value"`
	}{Value: 7}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRPCIncludesResponseBodyOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("conflict"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.RPC(context.Background(), "example", map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected response detail, got %v", err)
	}
}
