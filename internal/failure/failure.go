// Package failure names which layer of the system broke and says so in one
// plain sentence. When a run fails the person asking should learn whether the
// reasoning provider, the catalog tools, the merchant conversation, the
// payment gateway, or the database is at fault, not just that something did.
package failure

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Layer is the part of the system that failed, named by its role.
type Layer string

// The layers a run can fail in, in the order a request travels through them.
const (
	LayerReasoning    Layer = "reasoning layer"
	LayerCatalog      Layer = "catalog tool channel"
	LayerConversation Layer = "merchant conversation channel"
	LayerPayment      Layer = "payment layer"
	LayerLedger       Layer = "wallet ledger"
	LayerGate         Layer = "spend gate"
	LayerRecords      Layer = "records database"
)

// Error is a failure attributed to one layer.
type Error struct {
	Layer Layer
	Err   error
}

// Error reports the layer and the cause.
func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Layer) + " failed"
	}
	return string(e.Layer) + ": " + e.Err.Error()
}

// Unwrap exposes the cause so errors.Is and errors.As keep working.
func (e *Error) Unwrap() error { return e.Err }

// In attributes an error to a layer. A nil error stays nil so it can wrap a
// call result directly.
func In(layer Layer, err error) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return err
	}
	return &Error{Layer: layer, Err: err}
}

// Reasoning attributes an error to the model provider.
func Reasoning(err error) error { return In(LayerReasoning, err) }

// Catalog attributes an error to the catalog tool channel.
func Catalog(err error) error { return In(LayerCatalog, err) }

// Conversation attributes an error to the merchant conversation channel.
func Conversation(err error) error { return In(LayerConversation, err) }

// Payment attributes an error to the payment gateway.
func Payment(err error) error { return In(LayerPayment, err) }

// Ledger attributes an error to the wallet ledger.
func Ledger(err error) error { return In(LayerLedger, err) }

// Records attributes an error to the database.
func Records(err error) error { return In(LayerRecords, err) }

// LayerOf reports the layer an error was attributed to, and whether it was.
func LayerOf(err error) (Layer, bool) {
	var attributed *Error
	if errors.As(err, &attributed) {
		return attributed.Layer, true
	}
	return "", false
}

// Explain turns an error into a sentence a person can act on: which layer
// broke, what the cause looked like, and what to check.
func Explain(err error) string {
	if err == nil {
		return ""
	}
	layer, known := LayerOf(err)
	// A failure that crossed a process boundary arrives as text. When the far
	// side named its own layer, believe that over the channel it arrived on.
	if inner, remote := embeddedLayer(err.Error()); remote && inner != layer {
		shape, hint := diagnose(inner, err)
		where := string(inner)
		if layer == LayerConversation {
			where = "merchant's " + where
		}
		message := fmt.Sprintf("Failed in the %s: %s", where, shape)
		if hint != "" {
			message += "\n" + hint
		}
		return message
	}
	shape, hint := diagnose(layer, err)
	if !known {
		layer = "agent"
		if hint == "" {
			hint = "Check the process log for the failing step."
		}
	}
	message := fmt.Sprintf("Failed in the %s: %s", layer, shape)
	if hint != "" {
		message += "\n" + hint
	}
	return message
}

// embeddedLayer finds a layer name the far side wrote into its error text.
func embeddedLayer(text string) (Layer, bool) {
	lower := strings.ToLower(text)
	for _, layer := range []Layer{LayerReasoning, LayerPayment, LayerLedger, LayerGate, LayerRecords, LayerCatalog} {
		if strings.Contains(lower, strings.ToLower(string(layer))+":") {
			return layer, true
		}
	}
	return "", false
}

// diagnose reads the shape of a cause and pairs it with the check worth making
// for that layer.
func diagnose(layer Layer, err error) (shape, hint string) {
	text := err.Error()
	lower := strings.ToLower(text)

	switch {
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err) || strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "it did not answer in time", timeoutHint(layer)
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused") || strings.Contains(lower, "econnrefused"):
		return "nothing is listening on its address", reachHint(layer)
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "dns"):
		return "its address does not resolve", reachHint(layer)
	case strings.Contains(lower, "401") || strings.Contains(lower, "403") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden"):
		return "it rejected our credentials", credentialHint(layer)
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "quota"):
		return "it is rate limiting us", "Wait for the window to reset or switch to another model or key."
	case strings.Contains(lower, "500") || strings.Contains(lower, "502") || strings.Contains(lower, "503") || strings.Contains(lower, "504"):
		return "it returned a server error", "The far side is unhealthy. Retry, and check its own log."
	case strings.Contains(lower, "not configured") || strings.Contains(lower, "is required") || strings.Contains(lower, "missing"):
		return trim(text), configHint(layer)
	}
	return trim(text), layerHint(layer)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func timeoutHint(layer Layer) string {
	switch layer {
	case LayerReasoning:
		return "The model provider accepted the request but never finished. Check the model name and whether the provider is degraded."
	case LayerConversation:
		return "The merchant received the message but its own reasoning did not finish. Check the market process log for a provider error."
	case LayerCatalog:
		return "The catalog server is slow or wedged. Check the market process log."
	case LayerPayment:
		return "The payment gateway did not respond. No money moved."
	}
	return "Check the far side's log for the step that hung."
}

func reachHint(layer Layer) string {
	switch layer {
	case LayerConversation, LayerCatalog:
		return "The market process is not running or is on another port. Start it and confirm its address."
	case LayerReasoning:
		return "Check the provider base URL."
	case LayerRecords:
		return "Check the database URL."
	}
	return "Confirm the address and that the far side is running."
}

func credentialHint(layer Layer) string {
	switch layer {
	case LayerReasoning:
		return "Check the provider API key and that the key is allowed to use this model."
	case LayerConversation, LayerCatalog:
		return "The shared token differs between the two processes. Make them match."
	case LayerPayment:
		return "Check the payment key id and secret."
	case LayerRecords:
		return "Check the database service key."
	}
	return "Check the credentials for that layer."
}

func configHint(layer Layer) string {
	switch layer {
	case LayerReasoning:
		return "The model provider is not configured. Set the API key, base URL, and model name."
	case LayerConversation:
		return "The merchant conversation endpoint is not configured."
	case LayerCatalog:
		return "The catalog endpoint is not configured."
	}
	return "A required setting is missing for that layer."
}

func layerHint(layer Layer) string {
	switch layer {
	case LayerReasoning:
		return "This came from the model provider, not from our own rules."
	case LayerGate:
		return "The spend gate refused. That is a limit working, not a fault."
	case LayerLedger:
		return "No money moved. The ledger write was refused."
	}
	return ""
}

// trim keeps a cause short enough to read in a chat message.
func trim(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	const limit = 240
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
