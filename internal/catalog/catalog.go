// Package catalog exposes read-only product and stock operations.
package catalog

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"agentmart/internal/supabase"
)

// Product is the catalog representation used by service boundaries.
type Product struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Category         string  `json:"category"`
	PricePaise       int64   `json:"price_paise"`
	Stock            int     `json:"stock"`
	WarrantyYears    int     `json:"warranty_years"`
	TrustScore       int     `json:"trust_score"`
	ComboWith        *string `json:"combo_with"`
	ComboDiscountPct int     `json:"combo_discount_pct"`
}

// SearchRequest contains optional catalog filters.
type SearchRequest struct {
	Query         string
	Category      string
	MaxPricePaise int64
}

// StockResult reports whether a requested quantity is available.
type StockResult struct {
	Available bool `json:"available"`
	Stock     int  `json:"stock"`
}

// Service reads products from Supabase.
type Service struct {
	db *supabase.Client
}

// NewService constructs a catalog service.
func NewService(db *supabase.Client) *Service {
	return &Service{db: db}
}

// Search returns products matching the supplied filters.
func (s *Service) Search(ctx context.Context, req SearchRequest) ([]Product, error) {
	query := url.Values{}
	query.Set("select", "id,name,category,price_paise,stock,warranty_years,trust_score,combo_with,combo_discount_pct")
	query.Set("order", "name.asc")
	query.Set("limit", "50")
	if text := strings.TrimSpace(req.Query); text != "" {
		query.Set("or", fmt.Sprintf("(name.ilike.*%s*,category.ilike.*%s*)", escapeFilter(text), escapeFilter(text)))
	}
	if category := strings.TrimSpace(req.Category); category != "" {
		query.Set("category", "eq."+escapeFilter(category))
	}
	if req.MaxPricePaise > 0 {
		query.Set("price_paise", "lte."+strconv.FormatInt(req.MaxPricePaise, 10))
	}
	var products []Product
	if err := s.db.Get(ctx, "products", query, &products); err != nil {
		return nil, err
	}
	return products, nil
}

// Get returns one product by its identifier.
func (s *Service) Get(ctx context.Context, id string) (Product, error) {
	if strings.TrimSpace(id) == "" {
		return Product{}, fmt.Errorf("product id is required")
	}
	query := url.Values{}
	query.Set("select", "id,name,category,price_paise,stock,warranty_years,trust_score,combo_with,combo_discount_pct")
	query.Set("id", "eq."+escapeFilter(id))
	query.Set("limit", "1")
	var products []Product
	if err := s.db.Get(ctx, "products", query, &products); err != nil {
		return Product{}, err
	}
	if len(products) == 0 {
		return Product{}, fmt.Errorf("product %q not found", id)
	}
	return products[0], nil
}

// CheckStock reports stock for a product and requested quantity.
func (s *Service) CheckStock(ctx context.Context, id string, qty int) (StockResult, error) {
	if qty <= 0 {
		return StockResult{}, fmt.Errorf("quantity must be positive")
	}
	product, err := s.Get(ctx, id)
	if err != nil {
		return StockResult{}, err
	}
	return StockResult{Available: product.Stock >= qty, Stock: product.Stock}, nil
}

func escapeFilter(value string) string {
	return strings.NewReplacer("%", "\\%", "*", "\\*", ",", "\\,", "(", "\\(", ")", "\\)").Replace(value)
}
