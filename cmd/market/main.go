// The market binary serves the read-only catalog HTTP API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentmart/internal/campaigns"
	"agentmart/internal/catalog"
	"agentmart/internal/marketaudit"
	"agentmart/internal/marketauth"
	"agentmart/internal/marketgraph"
	"agentmart/internal/markettools"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
	"agentmart/internal/razorpay"
	buyerreasoning "agentmart/internal/reasoning"
	"agentmart/internal/supabase"
	"agentmart/internal/trading"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	db, err := supabase.NewClient(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_SECRET_KEY"), &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		logger.Error("market configuration failed", "error", err)
		return
	}
	service := catalog.NewService(db)
	store, err := negotiation.NewRedisSessionStore(os.Getenv("UPSTASH_REDIS_REST_URL"), os.Getenv("UPSTASH_REDIS_REST_TOKEN"), nil)
	if err != nil {
		logger.Error("market negotiation storage configuration failed", "error", err)
		return
	}
	addr := os.Getenv("MARKET_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	agentEndpoint := strings.TrimSuffix(os.Getenv("MARKET_AGENT_CARD_URL"), "/.well-known/agent-card.json")
	if agentEndpoint == "" {
		agentEndpoint = "http://localhost" + addr + "/a2a/"
	}
	if !strings.HasSuffix(agentEndpoint, "/") {
		agentEndpoint += "/"
	}
	merchantConfig := buyerreasoning.FromEnv("MARKET")
	// One campaign provider serves both the strategist's facts and the price floor,
	// so the discount a buyer is offered and the discount the floor allows come
	// from the same read.
	campaignProvider := campaigns.NewProvider(db)
	merchantNegotiator, nerr := marketgraph.New(marketgraph.Config{
		APIKey: merchantConfig.APIKey, BaseURL: merchantConfig.BaseURL, Model: merchantConfig.Model,
	}, campaignProvider, marketaudit.New(db))
	if nerr != nil {
		logger.Error("merchant negotiator configuration failed", "error", nerr)
		return
	}
	// marketgraph.New returns a nil pointer when no model is configured. Assign
	// through the interface only when it is real, otherwise the interface value
	// is non-nil while the pointer inside it is not.
	var merchant merchantBrain
	if merchantNegotiator != nil {
		merchant = merchantNegotiator
	}
	entitlement := func(ctx context.Context, accountID string) (int, error) {
		_, pct, _, err := campaignProvider.Eligibility(ctx, negotiation.CounterInput{BuyerAccountID: accountID})
		return pct, err
	}
	// The shop reads its own selling rate from its records and its refund rate from
	// the gateway. Credentials are optional here: without them the shop still prices
	// from how fast things move, just without knowing how much comes back.
	var tradingProvider *trading.Provider
	if gateway, gerr := razorpay.NewClient(os.Getenv("RAZORPAY_KEY_ID"), os.Getenv("RAZORPAY_KEY_SECRET"), &http.Client{Timeout: 10 * time.Second}); gerr == nil {
		tradingProvider = trading.NewProvider(db, gateway)
	} else {
		logger.Info("pricing without gateway refund figures", "reason", gerr)
		tradingProvider = trading.NewProvider(db, nil)
	}
	var conditions func(context.Context, string) (negotiation.TradingConditions, error)
	if tradingProvider != nil {
		conditions = tradingProvider.Conditions
	}
	handler, err := newHandler(service, store, agentEndpoint, os.Getenv("MARKET_SHARED_TOKEN"), merchant, entitlement, conditions)
	if err != nil {
		logger.Error("market handler configuration failed", "error", err)
		return
	}
	logger.Info("market listening", "addr", addr)
	server := &http.Server{Addr: addr, Handler: handler}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("market server stopped", "error", err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("market server shutdown failed", "error", err)
		}
	}
}

type catalogReader interface {
	Search(context.Context, catalog.SearchRequest) ([]catalog.Product, error)
	Get(context.Context, string) (catalog.Product, error)
	GetWithCost(context.Context, string) (catalog.Product, error)
	CheckStock(context.Context, string, int) (catalog.StockResult, error)
}

// merchantBrain is the merchant's reasoning: the shop-owner voice that shows
// stock and the strategist that prices it.
type merchantBrain interface {
	negotiation.Negotiator
	negotiation.Shopfront
}

func newHandler(service catalogReader, store negotiation.SessionStore, agentEndpoint, sharedToken string, merchantNegotiator merchantBrain, entitlement func(context.Context, string) (int, error), conditions func(context.Context, string) (negotiation.TradingConditions, error)) (http.Handler, error) {
	privateMux := http.NewServeMux()
	getPriced := func(ctx context.Context, id string) (catalog.Product, int64, error) {
		product, err := service.GetWithCost(ctx, id)
		if err != nil {
			return catalog.Product{}, 0, err
		}
		return product, product.CostPaise, nil
	}
	negotiationServer, err := negotiation.NewOrchestratedServer(service.Get, getPriced, store)
	if err != nil {
		return nil, err
	}
	// A funded loyalty discount is the only thing that lets a price settle below
	// the list total, so the floor reads the same campaign tier the strategist is
	// shown rather than trusting a number from a model.
	if entitlement != nil {
		negotiationServer.WithEntitlement(entitlement)
	}
	if conditions != nil {
		negotiationServer.WithConditions(conditions)
	}
	if merchantNegotiator != nil {
		negotiationServer.UseNegotiator(merchantNegotiator)
		// One merchant, one brain: the shop-owner voice answers browse turns
		// through the same server that quotes and negotiates.
		negotiationServer.WithShopfront(merchantNegotiator, service.Search)
	}
	privateMux.Handle("POST /negotiation", negotiationServer.Handler())
	// The agent surface shares that server, so a buyer talking agent to agent
	// reaches the same reasoning and the same cost floor as a direct caller.
	agentHandler, err := merchantagent.NewHandler(negotiationServer, agentEndpoint)
	if err != nil {
		return nil, err
	}
	// Register both spellings: a POST to the bare "/a2a" would otherwise hit
	// ServeMux's 301 redirect, which rewrites POST to GET and yields a confusing
	// 405 Method Not Allowed from the JSON-RPC handler.
	privateMux.Handle("/a2a", http.StripPrefix("/a2a", agentHandler))
	privateMux.Handle("/a2a/", http.StripPrefix("/a2a", agentHandler))
	mcpServer := markettools.NewServer(service)
	markettools.AddOffersTool(mcpServer, service)
	privateMux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true, PropagateRequestCancellation: true}))
	privateMux.HandleFunc("GET /catalog/search", func(w http.ResponseWriter, r *http.Request) {
		maxPrice, err := parseInt64(r.URL.Query().Get("max_price_paise"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "max_price_paise must be an integer")
			return
		}
		products, err := service.Search(r.Context(), catalog.SearchRequest{Query: r.URL.Query().Get("q"), Category: r.URL.Query().Get("category"), MaxPricePaise: maxPrice})
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, products)
	})
	privateMux.HandleFunc("GET /catalog/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		product, err := service.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, product)
	})
	privateMux.HandleFunc("GET /catalog/products/{id}/stock", func(w http.ResponseWriter, r *http.Request) {
		qty, err := strconv.Atoi(r.URL.Query().Get("qty"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "qty must be an integer")
			return
		}
		stock, err := service.CheckStock(r.Context(), r.PathValue("id"), qty)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, stock)
	})
	protected, err := marketauth.RequireBearer(sharedToken, privateMux)
	if err != nil {
		return nil, err
	}
	root := http.NewServeMux()
	root.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/", protected)
	return requestLogger(root), nil
}

func parseInt64(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("market request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}
