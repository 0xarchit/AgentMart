// Tests for trusted REST mutation requests.
package supabase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInsertUsesRepresentationPreference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/v1/link_tokens" {
			t.Fatalf("unexpected request")
		}
		if r.Header.Get("Prefer") != "return=representation" {
			t.Fatalf("missing representation preference")
		}
		_, _ = w.Write([]byte(`[{"token":"token"}]`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := client.Insert(context.Background(), "link_tokens", map[string]any{"token": "token"}, &rows); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rows)
	if string(encoded) != `[{"token":"token"}]` {
		t.Fatalf("rows = %s", encoded)
	}
}
