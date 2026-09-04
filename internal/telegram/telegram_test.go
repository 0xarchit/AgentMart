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

func TestSetWebhookRegistersOneOrderedConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; !strings.HasSuffix(got, "/setWebhook") {
			t.Fatalf("path = %s", got)
		}
		query := r.URL.Query()
		if query.Get("url") != "https://agents.example.com/telegram/webhook" {
			t.Fatalf("url = %q", query.Get("url"))
		}
		if query.Get("secret_token") != "shh" {
			t.Fatalf("secret_token = %q", query.Get("secret_token"))
		}
		// One connection keeps deliveries ordered, which is what lets the stored
		// offset stay the guard against a repeat.
		if query.Get("max_connections") != "1" {
			t.Fatalf("max_connections = %q, want 1", query.Get("max_connections"))
		}
		if query.Get("allowed_updates") != `["message","callback_query"]` {
			t.Fatalf("allowed_updates = %q", query.Get("allowed_updates"))
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	client, _ := NewClient("token", server.Client())
	client.baseURL = server.URL
	if err := client.SetWebhook(context.Background(), "https://agents.example.com/telegram/webhook", "shh"); err != nil {
		t.Fatal(err)
	}
}

func TestSetWebhookReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"description":"bad webhook: HTTPS url must be provided"}`))
	}))
	defer server.Close()
	client, _ := NewClient("token", server.Client())
	client.baseURL = server.URL
	err := client.SetWebhook(context.Background(), "https://agents.example.com/telegram/webhook", "shh")
	if err == nil || !strings.Contains(err.Error(), "HTTPS url must be provided") {
		t.Fatalf("err = %v, want the refusal quoted", err)
	}
}

func TestSetWebhookRefusesToRegisterWithoutASecret(t *testing.T) {
	client, _ := NewClient("token", nil)
	if err := client.SetWebhook(context.Background(), "https://agents.example.com/telegram/webhook", "  "); err == nil {
		t.Fatal("expected a webhook with no secret to be refused before it is registered")
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
