// Tests for reply formatting.
package main

import (
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
