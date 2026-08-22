// Tests for buyer command response selection.
package main

import "testing"

func TestResponseForCommand(t *testing.T) {
	if got := responseForCommand("/buy"); got == "" {
		t.Fatal("expected purchase response")
	}
	if got := responseForCommand("/unknown"); got != "Use /start, /link, or /buy." {
		t.Fatalf("unexpected fallback response: %q", got)
	}
}
