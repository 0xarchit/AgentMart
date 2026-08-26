// Package remotemerchant exposes the merchant's A2A service as a native ADK
// agent. The buyer graph can then delegate to the merchant the same way it
// delegates to any sub-agent, instead of hand-rolling JSON-RPC calls: ADK owns
// the task lifecycle, including TaskStateInputRequired hand-backs.
package remotemerchant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentmart/internal/marketauth"

	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/remoteagent/v2"
)

// Config points at the merchant's agent card and carries the shared token the
// market requires on private routes.
type Config struct {
	// Endpoint is the merchant agent-card URL or its A2A mount.
	Endpoint string
	// SharedToken is MARKET_SHARED_TOKEN; required when the market enforces it.
	SharedToken string
	// Timeout bounds card resolution and every delegated call.
	Timeout time.Duration
}

const defaultTimeout = 60 * time.Second

// New resolves the merchant agent card and wraps it as an ADK agent. It returns
// (nil, nil) when no endpoint is configured so callers can degrade to their
// local tools.
func New(ctx context.Context, cfg Config) (agent.Agent, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{Timeout: timeout}
	if strings.TrimSpace(cfg.SharedToken) != "" {
		authed, err := marketauth.NewClient(cfg.SharedToken, httpClient)
		if err != nil {
			return nil, fmt.Errorf("merchant agent auth: %w", err)
		}
		httpClient = authed
	}

	// Resolve the card with the authenticated client: the market keeps its
	// agent card behind the same shared-token wall as the JSON-RPC mount.
	card, err := agentcard.NewResolver(httpClient).Resolve(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("resolve merchant agent card: %w", err)
	}

	factory := a2aclient.NewFactory(a2aclient.WithJSONRPCTransport(httpClient))
	merchant, err := remoteagent.NewA2A(remoteagent.A2AConfig{
		Name:           "merchant_agent",
		Description:    "The merchant's own agent: quotes offers, explains bundle and warranty value, and answers counter-offers within its pricing rails.",
		AgentCard:      card,
		ClientProvider: remoteagent.NewA2AClientProvider(factory),
	})
	if err != nil {
		return nil, fmt.Errorf("create merchant remote agent: %w", err)
	}
	return merchant, nil
}
