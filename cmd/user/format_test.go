// Tests for reply formatting.
package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/telegram"
)

func TestAmountsAndIdentifiersAreMarkedUp(t *testing.T) {
	marked := highlight("Human approval required for INR 3149.20. Approval token: tok-9")
	if !strings.Contains(marked, "<b>INR 3149.20</b>") {
		t.Fatalf("the amount is not the weight of the message: %s", marked)
	}
	// An identifier the person may have to send back is one tap to copy rather
	// than a hand selection.
	if !strings.Contains(marked, "<code>tok-9</code>") {
		t.Fatalf("the token is not copyable: %s", marked)
	}
	order := highlight("Purchase fulfilled via wallet for INR 12.50. Order: order-1")
	if !strings.Contains(order, "<code>order-1</code>") || !strings.Contains(order, "<b>INR 12.50</b>") {
		t.Fatalf("order reply = %s", order)
	}
	if commands := highlight("Use /approve TOKEN or /reject TOKEN."); !strings.Contains(commands, "<code>/approve</code>") {
		t.Fatalf("commands are not marked: %s", commands)
	}
}

func TestMarkupCannotBeInjectedByAProductName(t *testing.T) {
	// A merchant names its own products and writes its own reasons. Escaping runs
	// first, so a name carrying a tag is shown as text rather than obeyed, and the
	// message is not rejected by the API for unbalanced markup.
	hostile := `Chose <b>Trim & Shave</b> <script>alert(1)</script> for INR 10.00`
	marked := highlight(telegram.Escape(hostile))
	if strings.Contains(marked, "<script>") {
		t.Fatalf("a product name injected markup: %s", marked)
	}
	if !strings.Contains(marked, "&lt;script&gt;") || !strings.Contains(marked, "&amp;") {
		t.Fatalf("escaping did not happen: %s", marked)
	}
	// The formatting this codebase adds itself still survives escaping.
	if !strings.Contains(marked, "<b>INR 10.00</b>") {
		t.Fatalf("the amount lost its weight: %s", marked)
	}
}

func TestClosingTagsAreNotReadAsCommands(t *testing.T) {
	// The command pattern looks for a slash, and every tag this adds contains one.
	// Marking commands before anything else is what keeps "</b>" intact.
	marked := highlight("Order: order-1 costs INR 5.00")
	for _, broken := range []string{"<code>/code</code>", "<code>/b</code>"} {
		if strings.Contains(marked, broken) {
			t.Fatalf("a closing tag was marked as a command: %s", marked)
		}
	}
}

// trackedBot records every call and answers sendMessage with a message id, so a
// test can tell a fresh message from an edit of an existing one.
func trackedBot(t *testing.T) (*telegram.Client, *telegramCalls) {
	t.Helper()
	record := &telegramCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		record.paths = append(record.paths, r.URL.Path)
		record.bodies = append(record.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":4242}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegram.NewClient("token", &http.Client{Transport: rewriteTelegramTransport{base: server.URL, next: server.Client().Transport}})
	if err != nil {
		t.Fatal(err)
	}
	return client, record
}

func TestProgressGrowsOneMessageInsteadOfStackingThem(t *testing.T) {
	client, record := trackedBot(t)
	notes := &liveNotes{client: client, chatID: 10}

	notes.add(t.Context(), "Asking the shop about a trimmer")
	notes.add(t.Context(), "The shop showed three options")
	notes.add(t.Context(), "Chose the Nova")

	sends, edits := 0, 0
	for _, path := range record.paths {
		switch {
		case strings.HasSuffix(path, "/sendMessage"):
			sends++
		case strings.HasSuffix(path, "/editMessageText"):
			edits++
		}
	}
	// One message, then edits of it. Three separate messages is the thing this
	// replaces.
	if sends != 1 || edits != 2 {
		t.Fatalf("sends = %d edits = %d, want 1 and 2, from %v", sends, edits, record.paths)
	}
	last := record.bodies[len(record.bodies)-1]
	for _, want := range []string{"Asking the shop", "three options", "Chose the Nova", "4242"} {
		if !strings.Contains(last, want) {
			t.Fatalf("the running note lost %q: %s", want, last)
		}
	}
}

func TestALongRunDoesNotGrowWithoutBound(t *testing.T) {
	client, record := trackedBot(t)
	notes := &liveNotes{client: client, chatID: 10}
	for i := 0; i < maxNoteLines+6; i++ {
		notes.add(t.Context(), fmt.Sprintf("step %d", i))
	}
	// The oldest lines fall off rather than pushing the message past what the API
	// will carry.
	last := record.bodies[len(record.bodies)-1]
	if strings.Contains(last, "step 0") {
		t.Fatalf("the note kept every line: %s", last)
	}
	if !strings.Contains(last, fmt.Sprintf("step %d", maxNoteLines+5)) {
		t.Fatalf("the note lost its newest line: %s", last)
	}
	if got := strings.Count(last, "step "); got > maxNoteLines {
		t.Fatalf("the note carries %d lines, want at most %d", got, maxNoteLines)
	}
}
