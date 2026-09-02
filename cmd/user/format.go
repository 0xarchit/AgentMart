// Reply formatting, so amounts and identifiers read as amounts and identifiers.
package main

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"

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

// maxNoteLines keeps the running note inside one screen. A long negotiation would
// otherwise grow past what the API will carry in a single message.
const maxNoteLines = 12

// liveNotes turns a run's progress into one message that grows, rather than a new
// message for every step. A run reports six or seven times, and six or seven
// separate bubbles bury the answer that arrives after them.
type liveNotes struct {
	client    *telegram.Client
	chatID    int64
	mu        sync.Mutex
	messageID int
	lines     []string
}

// add records one step and shows it. Losing the live view costs the person the
// commentary and nothing else, so a failure here is reported and stepped over.
func (n *liveNotes) add(ctx context.Context, line string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lines = append(n.lines, line)
	if len(n.lines) > maxNoteLines {
		n.lines = n.lines[len(n.lines)-maxNoteLines:]
	}
	body := highlight(telegram.Escape(strings.Join(n.lines, "\n")))

	working(ctx, n.client, n.chatID)
	if n.messageID == 0 {
		sent, err := n.client.SendTracked(ctx, n.chatID, body)
		if err != nil {
			log.Printf("send progress note failed: %v", err)
			return
		}
		n.messageID = sent
		return
	}
	if err := n.client.EditTracked(ctx, n.chatID, n.messageID, body); err != nil {
		log.Printf("update progress note failed: %v", err)
	}
}
