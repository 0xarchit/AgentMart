// Package negotiation models the merchant counter-offer state machine.
package negotiation

import (
	"fmt"
	"strings"
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
}

// Session is an in-memory negotiation task for one purchase attempt.
type Session struct {
	Proposal Proposal
	Counter  Counter
	Status   Status
}

// New creates a proposed negotiation session.
func New(proposal Proposal) (Session, error) {
	if strings.TrimSpace(proposal.ProductID) == "" || proposal.Quantity <= 0 || proposal.BaseAmountPaise <= 0 {
		return Session{}, fmt.Errorf("product, quantity, and base amount are required")
	}
	return Session{Proposal: proposal, Status: StatusProposed}, nil
}

// CounterOffer records a merchant counter amount that is not below the proposal.
func (s *Session) CounterOffer(counter Counter) error {
	if s.Status != StatusProposed {
		return fmt.Errorf("counter offer requires proposed state")
	}
	if counter.FinalAmountPaise < s.Proposal.BaseAmountPaise {
		return fmt.Errorf("counter amount cannot be below proposal")
	}
	if strings.TrimSpace(counter.Reason) == "" {
		return fmt.Errorf("counter reason is required")
	}
	s.Counter = counter
	s.Status = StatusCountered
	return nil
}

// Accept records buyer acceptance of the merchant counter.
func (s *Session) Accept() error {
	if s.Status != StatusCountered {
		return fmt.Errorf("accept requires countered state")
	}
	s.Status = StatusAccepted
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
	return nil
}

// UpliftPaise returns the merchant growth captured by an accepted counter.
func (s Session) UpliftPaise() int64 {
	if s.Status != StatusAccepted {
		return 0
	}
	return s.Counter.FinalAmountPaise - s.Proposal.BaseAmountPaise
}
