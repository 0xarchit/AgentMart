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
	policy     Policy
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
		case "accept", "decline":
			response, err = s.resolve(r.Context(), request)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be propose, accept, or decline"})
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
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"qty"`
	Reason    string `json:"reason"`
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
	counter, err := s.policy.Counter(product, request.Quantity)
	if err != nil {
		return nil, err
	}
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
	return map[string]any{
		"session_id":         sessionID,
		"type":               "counter",
		"product_id":         product.ID,
		"qty":                request.Quantity,
		"base_amount_paise":  session.Proposal.BaseAmountPaise,
		"final_amount_paise": counter.FinalAmountPaise,
		"reason":             counter.Reason,
	}, nil
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
