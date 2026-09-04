// The buyer agent's public service: card discovery, JSON-RPC handling, the
// bearer wall that keeps an unauthenticated agent from driving our shopper, and
// the Telegram webhook, which Telegram authenticates with its own secret because
// it cannot send ours.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agentmart/internal/buyeragent"
	"agentmart/internal/marketauth"
	"agentmart/internal/shopgraph"
)

// newBuyerAgentHandler builds the buyer's HTTP surface. /health is always
// answered. The agent's own routes sit behind the shared token; without a token
// the agent is not published at all, because a reachable shopper is a spending
// surface. The Telegram webhook, when one is configured, carries its own secret
// and so is mounted outside the bearer wall: Telegram cannot send our token.
func newBuyerAgentHandler(shopper buyeragent.Shopper, endpoint, sharedToken string, webhook http.Handler) (http.Handler, error) {
	root := http.NewServeMux()
	root.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "agent": "agentmart-buyer"})
	})
	if webhook != nil {
		root.Handle("POST "+telegramWebhookPath, webhook)
	}
	// The webhook is the bot's only way in once a public URL is set, so the surface
	// still stands when there is no agent to publish.
	if shopper == nil {
		return root, nil
	}
	if strings.TrimSpace(sharedToken) == "" {
		return nil, fmt.Errorf("buyer agent requires USER_AGENT_TOKEN")
	}
	agentHandler, err := buyeragent.NewHandler(shopper, endpoint)
	if err != nil {
		return nil, err
	}
	private := http.NewServeMux()
	private.Handle("/a2a/", http.StripPrefix("/a2a", agentHandler))
	private.Handle("/a2a", http.StripPrefix("/a2a", agentHandler))
	protected, err := marketauth.RequireBearer(sharedToken, private)
	if err != nil {
		return nil, err
	}
	root.Handle("/", protected)
	return root, nil
}

// shopperFunc adapts the graph service to the buyeragent surface.
type shopperFunc func(ctx context.Context, request string, wallet shopgraph.Wallet) (shopgraph.Result, error)

func (f shopperFunc) Run(ctx context.Context, request string, wallet shopgraph.Wallet) (shopgraph.Result, error) {
	return f(ctx, request, wallet)
}
