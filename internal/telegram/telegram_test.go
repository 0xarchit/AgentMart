// Tests for Telegram request construction.
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestAnswerCallbackQueryAcceptsTheBareTrueTheAPIReturns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; !strings.HasSuffix(got, "/answerCallbackQuery") {
			t.Fatalf("path = %s", got)
		}
		// This is the exact shape the API answers with, a bare boolean result.
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	client, _ := NewClient("token", server.Client())
	client.baseURL = server.URL
	if err := client.AnswerCallbackQuery(context.Background(), "cb-1"); err != nil {
		t.Fatalf("a bare true result must decode: %v", err)
	}
}

func TestAnswerCallbackQueryReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"description":"query is too old"}`))
	}))
	defer server.Close()
	client, _ := NewClient("token", server.Client())
	client.baseURL = server.URL
	err := client.AnswerCallbackQuery(context.Background(), "cb-2")
	if err == nil || !strings.Contains(err.Error(), "query is too old") {
		t.Fatalf("err = %v, want the refusal quoted", err)
	}
}
