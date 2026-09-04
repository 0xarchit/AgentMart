// The Telegram webhook surface. With a public URL the buyer stops asking for
// updates and Telegram posts them here, which is the only arrangement that works
// on a host that sleeps between requests: the delivery itself wakes the service.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"agentmart/internal/telegram"
)

// telegramWebhookPath is fixed so the URL registered with Telegram and the route
// served here cannot drift apart.
const telegramWebhookPath = "/telegram/webhook"

const (
	// webhookBodyLimit sits far above any real update and far below a body worth
	// allocating for.
	webhookBodyLimit = 1 << 20
	// webhookQueueDepth buffers deliveries while a shopping run is in flight. A run
	// can take minutes against a rate limited model, and Telegram will not wait for
	// it, so the reply to Telegram is the handover rather than the outcome.
	webhookQueueDepth = 32
)

// newWebhookHandler serves Telegram deliveries into the queue the buyer reads.
//
// Telegram cannot send our bearer token, so the endpoint authenticates the one
// way Telegram offers: the secret it echoes in a header on every delivery.
// Without that secret anyone who found the URL could forge a message from a
// linked person and spend that person's allowance, so a missing secret refuses to
// start rather than serving an open door.
func newWebhookHandler(secret string, deliveries chan<- telegram.Update, logger *slog.Logger) (http.Handler, error) {
	expected := []byte(strings.TrimSpace(secret))
	if len(expected) == 0 {
		return nil, fmt.Errorf("telegram webhook requires TELEGRAM_WEBHOOK_SECRET_TOKEN")
	}
	if deliveries == nil {
		return nil, fmt.Errorf("telegram webhook requires a delivery queue")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
		if subtle.ConstantTimeCompare(presented, expected) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var update telegram.Update
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, webhookBodyLimit)).Decode(&update); err != nil {
			http.Error(w, "malformed update", http.StatusBadRequest)
			return
		}
		select {
		case deliveries <- update:
			w.WriteHeader(http.StatusOK)
		default:
			// Telegram retries a delivery it could not hand over, so a full queue
			// delays an update instead of losing it. Answering 200 here would drop
			// somebody's message in silence.
			logger.Warn("telegram webhook queue is full, asking telegram to retry", "update_id", update.UpdateID)
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	}), nil
}

// webhookTarget picks the way in: the configured public url, or an empty string to
// keep polling. TELEGRAM_USE_POLLING wins over a url that is set, because running
// locally against a copy of the deployed environment would otherwise register a url
// this process can never be reached on and then wait for deliveries that land
// somewhere else. A value that is not a boolean is not treated as one.
func webhookTarget(configuredURL, usePolling string) string {
	if polling, err := strconv.ParseBool(strings.TrimSpace(usePolling)); err == nil && polling {
		return ""
	}
	return strings.TrimSpace(configuredURL)
}

// webhookEndpointURL turns the configured public URL into the exact URL Telegram
// should post to. Telegram requires HTTPS. A base URL with no path of its own is
// completed rather than registered as it stands, because the site root is behind
// the bearer wall and would answer every delivery with a 401.
func webhookEndpointURL(configured string) (string, error) {
	trimmed := strings.TrimSpace(configured)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse TELEGRAM_WEBHOOK_URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("telegram webhook needs an https url, got %q", trimmed)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("telegram webhook url has no host: %q", trimmed)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = telegramWebhookPath
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
