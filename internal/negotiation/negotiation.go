// Package negotiation models the merchant counter-offer state machine.
package negotiation

import (
	"fmt"
	"strings"
	"time"
)

// ErrInvalidProposal is returned when quantity or price cannot form a proposal.
var ErrInvalidProposal = fmt.Errorf("invalid proposal")

// Status describes the current negotiation state.
type Status string

const (
	StatusProposed  Status = "proposed"
	StatusCountered Status = "countered"
	StatusAccepted  Status = "accepted"
	StatusDeclined  Status = "declined"
)

// Proposal contains the buyer's original amount and requested quantity.
type Proposal struct {
	ProductID       string
	Quantity        int
	BaseAmountPaise int64
}

// Counter contains the merchant's accepted or pending uplift.
type Counter struct {
	FinalAmountPaise int64
	Reason           string
	// QuotedAt is when this amount became the standing ask. The session stamps it
	// rather than taking it from a caller, and it is reported to the buyer on
	// resolve, because a buyer accepting a stored quote from a command has no other
	// way to know how old the price is: the free-text path stamps receipt itself,
	// but /negotiate and /accept are separate messages with nothing between them.
	QuotedAt time.Time
}

// MaxRounds caps merchant counters after the opening offer.
const MaxRounds = 3

// Turn records one side of the negotiation conversation for transcript export.
type Turn struct {
	Actor   string    `json:"actor"` // buyer | merchant
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Session is an in-memory negotiation task for one purchase attempt.
type Session struct {
	Proposal   Proposal
	Counter    Counter
	Status     Status
	Round      int // number of merchant counters so far (opening = 1)
	Transcript []Turn
	// BuyerAccountID is recorded at propose time when the buyer agent
	// identifies itself, so later rounds can personalise campaign offers
	// without the buyer resending identity on every message.
	BuyerAccountID string `json:"buyer_account_id,omitempty"`
	// FloorPaise is the lowest total this session may record, which is the cost
	// floor raised to the list total less whatever discount this buyer is funded
	// for. It is written by the caller that computed it, because the floor needs
	// the product's cost and the buyer's campaign and the session knows neither.
	FloorPaise int64 `json:"floor_amount_paise,omitempty"`
	// BundledPaise is what the opening offer charged for goods attached to the
	// product the buyer named. It sits beside the proposal rather than inside it
	// because the proposal's base amount has to stay the main product alone: the
	// gate re-derives that from the unit price and the quantity, and a base that
	// included a partner would fail its own arithmetic check. The funded discount
	// needs the other figure, the list total of everything being bought, or a
	// bundled deal concedes further than the campaign funds.
	BundledPaise int64 `json:"bundled_amount_paise,omitempty"`
}

// New creates a proposed negotiation session.
func New(proposal Proposal) (Session, error) {
	if strings.TrimSpace(proposal.ProductID) == "" || proposal.Quantity <= 0 || proposal.BaseAmountPaise <= 0 {
		return Session{}, fmt.Errorf("product, quantity, and base amount are required")
	}
	return Session{Proposal: proposal, Status: StatusProposed}, nil
}

// CounterOffer records a merchant counter amount that is not below the floor.
func (s *Session) CounterOffer(counter Counter) error {
	if s.Status != StatusProposed {
		return fmt.Errorf("counter offer requires proposed state")
	}
	if err := validateCounter(s, counter); err != nil {
		return err
	}
	counter.QuotedAt = time.Now().UTC()
	s.Counter = counter
	s.Status = StatusCountered
	s.Round = 1
	s.appendTurn("merchant", fmt.Sprintf("Offer INR %.2f: %s", float64(counter.FinalAmountPaise)/100, counter.Reason))
	return nil
}

// Renegotiate replaces the pending counter during an in-progress negotiation.
// It enforces the orchestrator's round cap; the caller computes the new amount.
func (s *Session) Renegotiate(counter Counter) error {
	if s.Status != StatusCountered {
		return fmt.Errorf("renegotiation requires countered state")
	}
	if s.Round >= MaxRounds {
		return fmt.Errorf("negotiation round limit reached")
	}
	if err := validateCounter(s, counter); err != nil {
		return err
	}
	counter.QuotedAt = time.Now().UTC()
	s.Counter = counter
	s.Round++
	s.appendTurn("merchant", fmt.Sprintf("Counter INR %.2f: %s", float64(counter.FinalAmountPaise)/100, counter.Reason))
	return nil
}

func validateCounter(s *Session, counter Counter) error {
	// The merchant's floor is what a recorded amount may not cross. With no floor
	// written the list total stands in, which is what a caller without a funded
	// entitlement gets and what this check enforced for everyone before.
	floor := s.FloorPaise
	if floor <= 0 {
		floor = s.Proposal.BaseAmountPaise
	}
	if counter.FinalAmountPaise < floor {
		return fmt.Errorf("counter amount cannot be below the merchant floor")
	}
	if strings.TrimSpace(counter.Reason) == "" {
		return fmt.Errorf("counter reason is required")
	}
	return nil
}

// RecordBuyer appends a buyer-side turn (counter proposal, accept, or decline).
func (s *Session) RecordBuyer(message string) {
	s.appendTurn("buyer", message)
}

func (s *Session) appendTurn(actor, message string) {
	s.Transcript = append(s.Transcript, Turn{Actor: actor, Message: message, At: time.Now().UTC()})
}

// Accept records buyer acceptance of the merchant counter.
func (s *Session) Accept() error {
	if s.Status != StatusCountered {
		return fmt.Errorf("accept requires countered state")
	}
	s.Status = StatusAccepted
	s.appendTurn("buyer", fmt.Sprintf("Accepted INR %.2f", float64(s.Counter.FinalAmountPaise)/100))
	return nil
}

// Decline records buyer rejection of the merchant counter.
func (s *Session) Decline(reason string) error {
	if s.Status != StatusCountered {
		return fmt.Errorf("decline requires countered state")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("decline reason is required")
	}
	s.Status = StatusDeclined
	s.appendTurn("buyer", fmt.Sprintf("Declined: %s", reason))
	return nil
}

// UpliftPaise returns the merchant growth captured by an accepted counter.
func (s Session) UpliftPaise() int64 {
	if s.Status != StatusAccepted {
		return 0
	}
	return s.Counter.FinalAmountPaise - s.Proposal.BaseAmountPaise
}
