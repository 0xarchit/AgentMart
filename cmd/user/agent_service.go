// The buyer agent's public service: card discovery, JSON-RPC handling, and the
// bearer wall that keeps an unauthenticated agent from driving our shopper.
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

// newBuyerAgentHandler builds the buyer agent mux. Everything except /health sits
// behind the shared token; without a token the service is not exposed at all,
// because a reachable shopper is a spending surface.
func newBuyerAgentHandler(shopper buyeragent.Shopper, endpoint, sharedToken string) (http.Handler, error) {
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
	root := http.NewServeMux()
	root.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "agent": "agentmart-buyer"})
	})
	root.Handle("/", protected)
	return root, nil
}

// shopperFunc adapts the graph service to the buyeragent surface.
type shopperFunc func(ctx context.Context, request string, wallet shopgraph.Wallet) (shopgraph.Result, error)

func (f shopperFunc) Run(ctx context.Context, request string, wallet shopgraph.Wallet) (shopgraph.Result, error) {
	return f(ctx, request, wallet)
}
