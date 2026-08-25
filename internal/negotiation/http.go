// HTTP handlers expose the negotiation task contract used by the merchant boundary.
package negotiation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agentmart/internal/catalog"
)

var (
	errProductNotFound = errors.New("product not found")
	errSessionNotFound = errors.New("session not found")
)

// Server stores demo negotiation sessions behind a small JSON API.
type Server struct {
	store      SessionStore
	products   map[string]catalog.Product
	getProduct func(context.Context, string) (catalog.Product, error)
	getPriced  func(context.Context, string) (catalog.Product, int64, error)
	negotiator Negotiator
	policy     Policy
}

// Negotiator lets the merchant agent choose counter amounts and wording inside
// the orchestrator's rails (>= floor, <= ask, round cap). Implementations must
// degrade to the deterministic concession schedule without an LLM.
type Negotiator interface {
	Counter(ctx context.Context, input CounterInput) (CounterOutput, error)
}

// CounterInput carries everything a merchant negotiator may use.
type CounterInput struct {
	Session            Session
	Product            catalog.Product
	Partner            *catalog.Product
	FloorPaise         int64 // never go below
	AskPaise           int64 // current standing offer
	BuyerPaise         int64 // buyer's latest counter
	MinAcceptablePaise int64 // orchestrator's concede schedule for this round
}

// CounterOutput is one merchant counter. Amounts outside the rails are clamped.
type CounterOutput struct {
	AmountPaise int64
	Reason      string
}

// NewCatalogServer constructs a negotiation API backed by an authoritative catalog reader.
func NewCatalogServer(getProduct func(context.Context, string) (catalog.Product, error)) *Server {
	return &Server{store: newMemorySessionStore(), getProduct: getProduct}
}

// NewCatalogServerWithStore constructs a catalog-backed API with durable session storage.
func NewCatalogServerWithStore(getProduct func(context.Context, string) (catalog.Product, error), store SessionStore) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("negotiation session store is required")
	}
	return &Server{store: store, getProduct: getProduct}, nil
}

// NewOrchestratedServer constructs the full merchant negotiation stack: durable
// sessions, cost-aware floors, and a pluggable LLM negotiator.
func NewOrchestratedServer(getProduct func(context.Context, string) (catalog.Product, error), getPriced func(context.Context, string) (catalog.Product, int64, error), store SessionStore) (*Server, error) {
	srv, err := NewCatalogServerWithStore(getProduct, store)
	if err != nil {
		return nil, err
	}
	srv.getPriced = getPriced
	return srv, nil
}

// UseNegotiator attaches the merchant agent used for counter amounts.
func (s *Server) UseNegotiator(n Negotiator) *Server {
	s.negotiator = n
	return s
}

// NewServer constructs a negotiation API with a catalog snapshot.
func NewServer(products []catalog.Product) *Server {
	indexed := make(map[string]catalog.Product, len(products))
	for _, product := range products {
		indexed[product.ID] = product
	}
	return &Server{store: newMemorySessionStore(), products: indexed}
}

// Handler returns the negotiation JSON handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/negotiation" {
			http.NotFound(w, r)
			return
		}

		var request negotiationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}

		var response any
		var err error
		switch request.Type {
		case "propose":
			response, err = s.propose(r.Context(), request)
		case "counter":
			response, err = s.counter(r.Context(), request)
		case "accept", "decline":
			response, err = s.resolve(r.Context(), request)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be propose, counter, accept, or decline"})
			return
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errProductNotFound) || errors.Is(err, errSessionNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
	})
}

type negotiationRequest struct {
	Type               string `json:"type"`
	SessionID          string `json:"session_id"`
	ProductID          string `json:"product_id"`
	Quantity           int    `json:"qty"`
	Reason             string `json:"reason"`
	CounterAmountPaise int64  `json:"counter_amount_paise"`
}

func (s *Server) propose(ctx context.Context, request negotiationRequest) (map[string]any, error) {
	var product catalog.Product
	var ok bool
	if s.getProduct != nil {
		loaded, err := s.getProduct(ctx, request.ProductID)
		if err != nil {
			return nil, errProductNotFound
		}
		product = loaded
		ok = true
	} else {
		product, ok = s.products[request.ProductID]
	}
	if !ok {
		return nil, errProductNotFound
	}
	if request.Quantity <= 0 {
		return nil, ErrInvalidProposal
	}
	var (
		offer   Offer
		err     error
		partner *catalog.Product
	)
	main, mainCost, pricedErr := s.pricedProduct(ctx, request.ProductID, product)
	if pricedErr != nil {
		return nil, pricedErr
	}
	if product.ComboWith != nil && product.ComboDiscountPct > 0 {
		loaded, partnerErr := s.getProduct(ctx, *product.ComboWith)
		if partnerErr == nil {
			partner = &loaded
		}
	}
	var partnerPriced *Priced
	if partner != nil && s.getPriced != nil {
		pp, pc, perr := s.getPriced(ctx, partner.ID)
		if perr == nil {
			partnerPriced = &Priced{Product: pp, CostPaise: pc}
		}
	}
	offer, err = OpeningOffer(Priced{Product: main, CostPaise: mainCost}, partnerPriced, request.Quantity)
	if err != nil {
		return nil, err
	}
	counter := Counter{FinalAmountPaise: offer.FinalPaise, Reason: offer.Reason}
	session, err := New(Proposal{
		ProductID:       product.ID,
		Quantity:        request.Quantity,
		BaseAmountPaise: product.PricePaise * int64(request.Quantity),
	})
	if err != nil {
		return nil, err
	}
	if err := session.CounterOffer(counter); err != nil {
		return nil, err
	}
	sessionID, err := newSessionID()
	if err != nil {
		return nil, err
	}
	if err := s.store.Put(ctx, sessionID, session); err != nil {
		return nil, err
	}
	response := map[string]any{
		"session_id":         sessionID,
		"type":               string(offer.Kind),
		"product_id":         product.ID,
		"qty":                request.Quantity,
		"base_amount_paise":  session.Proposal.BaseAmountPaise,
		"final_amount_paise": counter.FinalAmountPaise,
		"reason":             counter.Reason,
		"offer_reason":       counter.Reason,
		"name":               product.Name,
		"category":           product.Category,
		"stock":              product.Stock,
		"warranty_years":     product.WarrantyYears,
		"trust_score":        product.TrustScore,
		"combo_with":         product.ComboWith,
		"combo_discount_pct": product.ComboDiscountPct,
		"rounds_left":        MaxRounds - session.Round,
	}
	if offer.Bundle != nil {
		response["bundle"] = offer.Bundle
	}
	return response, nil
}

// counter evaluates a buyer counter through the orchestrator rails and asks the
// merchant agent for wording/amount inside those rails when one is attached.
func (s *Server) counter(ctx context.Context, request negotiationRequest) (map[string]any, error) {
	session, ok, err := s.store.Get(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errSessionNotFound
	}
	if session.Status != StatusCountered {
		return nil, fmt.Errorf("counter requires countered state")
	}
	if request.CounterAmountPaise <= 0 {
		return nil, ErrInvalidProposal
	}
	product, _, perr := s.pricedProduct(ctx, session.Proposal.ProductID, catalog.Product{})
	if perr != nil {
		return nil, perr
	}
	var partner *catalog.Product
	if product.ComboWith != nil && product.ComboDiscountPct > 0 {
		if loaded, perr := s.getProduct(ctx, *product.ComboWith); perr == nil {
			partner = &loaded
		}
	}
	floorPaise, ferr := s.floorFor(ctx, session.Proposal.ProductID, session.Proposal.Quantity, product.ComboWith, product.ComboDiscountPct)
	if ferr != nil {
		return nil, ferr
	}
	ask := session.Counter.FinalAmountPaise
	session.RecordBuyer(fmt.Sprintf("Counters INR %.2f", float64(request.CounterAmountPaise)/100))

	decision := Decide(session, request.CounterAmountPaise, floorPaise)
	if decision.Accepted {
		session.Counter.FinalAmountPaise = decision.FinalPaise
		session.Counter.Reason = decision.Reason
		if err := session.Accept(); err != nil {
			return nil, err
		}
		if err := s.store.Put(ctx, request.SessionID, session); err != nil {
			return nil, err
		}
		return map[string]any{
			"session_id":         request.SessionID,
			"status":             StatusAccepted,
			"final_amount_paise": decision.FinalPaise,
			"reason":             decision.Reason,
		}, nil
	}

	amount, reason := decision.FinalPaise, decision.Reason
	if s.negotiator != nil && !decision.Exhausted {
		minAcceptable := amount // orchestrator already computed this round's rail
		if out, nerr := s.negotiator.Counter(ctx, CounterInput{
			Session: session, Product: product, Partner: partner,
			FloorPaise: floorPaise, AskPaise: ask,
			BuyerPaise: request.CounterAmountPaise, MinAcceptablePaise: minAcceptable,
		}); nerr == nil && out.AmountPaise > 0 {
			amount = clampCounter(out.AmountPaise, maxInt64(floorPaise, request.CounterAmountPaise), ask)
			if strings.TrimSpace(out.Reason) != "" {
				reason = out.Reason
			}
		}
	}
	if decision.Exhausted {
		if derr := session.Decline(reason); derr != nil {
			return nil, derr
		}
		if err := s.store.Put(ctx, request.SessionID, session); err != nil {
			return nil, err
		}
		return map[string]any{
			"session_id":         request.SessionID,
			"status":             StatusDeclined,
			"reason":             reason,
			"floor_amount_paise": floorPaise,
		}, nil
	}
	if rerr := session.Renegotiate(Counter{FinalAmountPaise: amount, Reason: reason}); rerr != nil {
		return nil, rerr
	}
	if err := s.store.Put(ctx, request.SessionID, session); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":         request.SessionID,
		"status":             StatusCountered,
		"final_amount_paise": amount,
		"reason":             reason,
		"rounds_left":        MaxRounds - session.Round,
	}, nil
}

func clampCounter(value, low, high int64) int64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// pricedProduct loads cost-aware facts; falls back to zero-cost when no priced
// reader is wired (fixture paths), which makes the floor zero.
func (s *Server) pricedProduct(ctx context.Context, id string, fallback catalog.Product) (catalog.Product, int64, error) {
	if s.getPriced != nil {
		return s.getPriced(ctx, id)
	}
	if fallback.ID == "" && s.getProduct != nil {
		loaded, err := s.getProduct(ctx, id)
		if err != nil {
			return catalog.Product{}, 0, errProductNotFound
		}
		return loaded, 0, nil
	}
	return fallback, 0, nil
}

// floorFor computes the blended loss boundary for a proposal, combo included.
func (s *Server) floorFor(ctx context.Context, productID string, qty int, comboWith *string, discountPct int) (int64, error) {
	main, mainCost, err := s.pricedProduct(ctx, productID, catalog.Product{})
	if err != nil {
		return 0, err
	}
	var partner *catalog.Product
	if comboWith != nil && discountPct > 0 {
		if loaded, perr := s.getProduct(ctx, *comboWith); perr == nil {
			partner = &loaded
		}
	}
	var partnerCost int64
	if partner != nil && s.getPriced != nil {
		_, pc, perr := s.getPriced(ctx, partner.ID)
		if perr != nil {
			return 0, perr
		}
		partnerCost = pc
	}
	return FloorFor(Priced{Product: main, CostPaise: mainCost}, partnerPricedOrNil(partner, partnerCost), qty)
}

func partnerPricedOrNil(partner *catalog.Product, cost int64) *Priced {
	if partner == nil {
		return nil
	}
	return &Priced{Product: *partner, CostPaise: cost}
}

func (s *Server) resolve(ctx context.Context, request negotiationRequest) (map[string]any, error) {
	session, ok, err := s.store.Get(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errSessionNotFound
	}
	if request.Type == "accept" {
		err = session.Accept()
	} else {
		err = session.Decline(request.Reason)
	}
	if err != nil {
		return nil, err
	}
	if err := s.store.Put(ctx, request.SessionID, session); err != nil {
		return nil, err
	}
	return map[string]any{
		"session_id":         request.SessionID,
		"status":             session.Status,
		"product_id":         session.Proposal.ProductID,
		"qty":                session.Proposal.Quantity,
		"uplift_paise":       session.UpliftPaise(),
		"base_amount_paise":  session.Proposal.BaseAmountPaise,
		"final_amount_paise": session.Counter.FinalAmountPaise,
		"transcript":         session.Transcript,
	}, nil
}

func newSessionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
