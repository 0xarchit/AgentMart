// Tests for Telegram request construction.
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPollUsesOffsetAndLongPolling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "4" || r.URL.Query().Get("timeout") != "30" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	client, _ := NewClient("token", server.Client())
	client.baseURL = server.URL
	if _, err := client.Poll(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
}
