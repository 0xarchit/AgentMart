// Tests for buyer command response selection.
package main

import (
	"context"
	"errors"
	"testing"
)

type fakeLinker struct{ err error }

func (f fakeLinker) Redeem(context.Context, string, int64) (string, error) {
	return "account", f.err
}

func TestResponseForCommand(t *testing.T) {
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, 1, []string{"/buy"}); got == "" {
		t.Fatal("expected purchase response")
	}
	if got, _ := responseForCommand(t.Context(), fakeLinker{}, 1, []string{"/unknown"}); got != "Use /start, /link TOKEN, or /buy." {
		t.Fatalf("unexpected fallback response: %q", got)
	}
}

func TestLinkCommand(t *testing.T) {
	got, _ := responseForCommand(t.Context(), fakeLinker{}, 10, []string{"/link", "token"})
	if got != "Telegram is now linked to your AgentMart wallet." {
		t.Fatalf("response = %q", got)
	}
	got, _ = responseForCommand(t.Context(), fakeLinker{err: errors.New("expired")}, 10, []string{"/link", "token"})
	if got != "That link token is invalid, expired, or already used." {
		t.Fatalf("response = %q", got)
	}
}
