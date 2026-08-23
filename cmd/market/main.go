// The market binary serves the read-only catalog HTTP API.
package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiation"
	"agentmart/internal/supabase"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	db, err := supabase.NewClient(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_SECRET_KEY"), &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		logger.Error("market configuration failed", "error", err)
		return
	}
	service := catalog.NewService(db)
	handler := newHandler(service)
	addr := os.Getenv("MARKET_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	logger.Info("market listening", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("market stopped", "error", err)
	}
}

func newHandler(service *catalog.Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /negotiation", negotiation.NewCatalogServer(service.Get).Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /catalog/search", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /catalog/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		product, err := service.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, product)
	})
	mux.HandleFunc("GET /catalog/products/{id}/stock", func(w http.ResponseWriter, r *http.Request) {
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
	return requestLogger(mux)
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
