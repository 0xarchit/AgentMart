// Reply formatting, so amounts and identifiers read as amounts and identifiers.
package main

import (
	"context"
	"regexp"

	"agentmart/internal/telegram"
)

// Applied to text that has already been escaped, so a product name or a
// merchant's own words cannot introduce markup of their own.
var (
	amountPattern  = regexp.MustCompile(`INR \d+\.\d{2}`)
	labelledID     = regexp.MustCompile(`(Approval token: |Order: |Session: )(\S+)`)
	commandPattern = regexp.MustCompile(`(^|[\s(])(/[a-z]+)`)
)

// highlight marks up the parts of a reply a person is looking for. Amounts carry
// the weight of the message, and an identifier they may have to send back is
// wrapped so it can be copied with one tap rather than selected by hand.
//
// This runs on escaped text on purpose. Formatting first and escaping afterwards
// would escape the markup, and escaping nothing at all would let a bracket in a
// product name have the whole message rejected by the API.
func highlight(escaped string) string {
	marked := commandPattern.ReplaceAllString(escaped, "$1<code>$2</code>")
	marked = amountPattern.ReplaceAllString(marked, "<b>$0</b>")
	return labelledID.ReplaceAllString(marked, "$1<code>$2</code>")
}

// sendReply sends one reply as rich text, escaped and marked up.
func sendReply(ctx context.Context, client *telegram.Client, chatID int64, text string, markup *telegram.InlineKeyboardMarkup) error {
	return client.SendRich(ctx, chatID, highlight(telegram.Escape(text)), markup)
}

// working shows the person that a run is under way. Telegram clears the
// indicator by itself after a few seconds, so this is called again as the run
// reports progress. It returns nothing: losing an animation is not a failure
// worth interrupting a purchase for.
func working(ctx context.Context, client *telegram.Client, chatID int64) {
	_ = client.SendChatAction(ctx, chatID, "typing")
}
