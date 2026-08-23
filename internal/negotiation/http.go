// HTTP handlers expose the negotiation task contract used by the merchant boundary.
package negotiation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"agentmart/internal/catalog"
)

var (
	errProductNotFound = errors.New("product not found")
	errSessionNotFound = errors.New("session not found")
)

// Server stores demo negotiation sessions behind a small JSON API.
type Server struct {
	mu         sync.Mutex
	sessions   map[string]Session
	products   map[string]catalog.Product
	getProduct func(context.Context, string) (catalog.Product, error)
	policy     Policy
}

// NewCatalogServer constructs a negotiation API backed by an authoritative catalog reader.
func NewCatalogServer(getProduct func(context.Context, string) (catalog.Product, error)) *Server {
	return &Server{sessions: make(map[string]Session), getProduct: getProduct}
}

// NewServer constructs a negotiation API with a catalog snapshot.
func NewServer(products []catalog.Product) *Server {
	indexed := make(map[string]catalog.Product, len(products))
	for _, product := range products {
		indexed[product.ID] = product
	}
	return &Server{sessions: make(map[string]Session), products: indexed}
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

		s.mu.Lock()
		defer s.mu.Unlock()

		var response any
		var err error
		switch request.Type {
		case "propose":
			response, err = s.propose(r.Context(), request)
		case "accept", "decline":
			response, err = s.resolve(request)
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
	s.sessions[sessionID] = session
	return map[string]any{
		"session_id":         sessionID,
		"type":               "counter",
		"product_id":         product.ID,
		"base_amount_paise":  session.Proposal.BaseAmountPaise,
		"final_amount_paise": counter.FinalAmountPaise,
		"reason":             counter.Reason,
	}, nil
}

func (s *Server) resolve(request negotiationRequest) (map[string]any, error) {
	session, ok := s.sessions[request.SessionID]
	if !ok {
		return nil, errSessionNotFound
	}
	var err error
	if request.Type == "accept" {
		err = session.Accept()
	} else {
		err = session.Decline(request.Reason)
	}
	if err != nil {
		return nil, err
	}
	s.sessions[request.SessionID] = session
	return map[string]any{
		"session_id":         request.SessionID,
		"status":             session.Status,
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
