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
	"syscall"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/marketauth"
	"agentmart/internal/markettools"
	"agentmart/internal/merchantagent"
	"agentmart/internal/negotiation"
	"agentmart/internal/supabase"
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
	agentEndpoint := os.Getenv("MARKET_AGENT_CARD_URL")
	if agentEndpoint == "" {
		agentEndpoint = "http://localhost" + addr + "/a2a"
	}
	handler, err := newHandler(service, store, agentEndpoint, os.Getenv("MARKET_SHARED_TOKEN"))
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
	CheckStock(context.Context, string, int) (catalog.StockResult, error)
}

func newHandler(service catalogReader, store negotiation.SessionStore, agentEndpoint, sharedToken string) (http.Handler, error) {
	privateMux := http.NewServeMux()
	negotiationServer, err := negotiation.NewCatalogServerWithStore(service.Get, store)
	if err != nil {
		return nil, err
	}
	privateMux.Handle("POST /negotiation", negotiationServer.Handler())
	agentHandler, err := merchantagent.NewHandler(service.Get, store, agentEndpoint)
	if err != nil {
		return nil, err
	}
	privateMux.Handle("/a2a/", http.StripPrefix("/a2a", agentHandler))
	mcpServer := markettools.NewServer(service)
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
