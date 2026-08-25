// Merchant negotiation orchestrator: concede on a schedule, never below cost.
package negotiation

import "fmt"

// Decision is the orchestrator's answer to one buyer counter.
type Decision struct {
	// Accepted is true when the buyer amount clears this round's minimum.
	Accepted bool
	// FinalPaise is the agreed amount when Accepted, else the new counter.
	FinalPaise int64
	Reason     string
	// Exhausted marks that all rounds are spent and the floor held.
	Exhausted bool
}

// concedeSchedule is the fraction of (ask-floor) the merchant still demands at
// each round: round 1 holds firm, then concedes 60%, then 85% toward the floor.
var concedeSchedule = [MaxRounds]float64{1.0, 0.6, 0.15}

// Decide evaluates one buyer counter against the session state and floor.
func Decide(s Session, buyerPaise int64, floor int64) Decision {
	ask := s.Counter.FinalAmountPaise
	if ask <= 0 {
		return Decision{Accepted: false, FinalPaise: floor, Reason: "session has no active offer"}
	}
	if floor < 0 {
		floor = 0
	}
	if buyerPaise >= ask {
		return Decision{Accepted: true, FinalPaise: buyerPaise, Reason: "buyer met the asking price"}
	}
	round := s.Round
	if round < 1 {
		round = 1
	}
	index := round - 1
	if index > MaxRounds-1 {
		index = MaxRounds - 1
	}
	span := ask - floor
	minimum := floor + int64(float64(span)*concedeSchedule[index])

	if buyerPaise >= minimum {
		return Decision{Accepted: true, FinalPaise: buyerPaise, Reason: "negotiated discount within margin"}
	}
	if s.Round >= MaxRounds {
		return Decision{
			Accepted:   false,
			FinalPaise: floor,
			Reason:     "below margin after all rounds; holding at cost floor",
			Exhausted:  true,
		}
	}
	counter := buyerPaise + int64(float64(minimum-buyerPaise)*0.5)
	if counter < floor {
		counter = floor
	}
	return Decision{
		Accepted:   false,
		FinalPaise: counter,
		Reason:     fmt.Sprintf("moving toward our best price; %d rounds left", MaxRounds-s.Round),
	}
}
