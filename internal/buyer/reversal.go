// The reversal side of the money path. Cancelling an order credits the allowance
// internally, and this turns that into a reversal the gateway can confirm, against
// the captured payments that funded the allowance in the first place.
package buyer

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/razorpay"
)

// FundingPayment is one captured payment that put money into an allowance.
type FundingPayment struct {
	PaymentID   string
	AmountPaise int64
}

// fundingReader lists the payments that funded an allowance, oldest first, so a
// reversal draws them down in the order they arrived.
type fundingReader interface {
	FundingPayments(context.Context, string) ([]FundingPayment, error)
}

// paymentReverser is the gateway calls this needs and nothing more.
type paymentReverser interface {
	Payment(context.Context, string) (razorpay.CapturedPayment, error)
	PaymentRefunds(context.Context, string) ([]razorpay.Refund, error)
	CreateRefund(context.Context, string, int64, string, map[string]string) (razorpay.Refund, error)
}

// ReverseRequest is an amount already credited back internally. Every field of it
// ends up in the gateway request, and a repeat of an interrupted reversal has to
// reproduce that request exactly, so the run is carried here with the rest rather
// than read from the surrounding context: the run in front of us is not the run
// the first attempt used.
type ReverseRequest struct {
	AccountID      string
	OrderID        string
	AmountPaise    int64
	IdempotencyKey string
	Reason         string
	RunID          string
}

// ReverseResult reports what the gateway confirmed, including anything an
// interrupted earlier attempt had already put there. A shortfall is stated rather
// than hidden: the internal credit has already happened either way, and a reader
// deserves to know the evidence is incomplete.
type ReverseResult struct {
	RefundIDs      []string
	ReversedPaise  int64
	ShortfallPaise int64
}

// Reversal turns an internal credit into gateway evidence. A nil implementation
// leaves the allowance path exactly as it was.
type Reversal interface {
	Reverse(context.Context, ReverseRequest) (ReverseResult, error)
}

// GatewayReversal refunds the captured payments that funded the allowance.
type GatewayReversal struct {
	funding fundingReader
	gateway paymentReverser
}

// NewGatewayReversal builds the reversal path.
func NewGatewayReversal(funding fundingReader, gateway paymentReverser) *GatewayReversal {
	return &GatewayReversal{funding: funding, gateway: gateway}
}

// Reverse draws the amount down across funding payments, oldest first, taking from
// each only what it still has left to give. It stops as soon as the amount is
// covered, so a single funding payment is the ordinary case and one call.
//
// The amount is always the whole sum credited back, never a remainder. What an
// earlier attempt already sent is worked out here instead, so calling this twice
// for one cancelled order reverses the money once. The request that arrives second
// is therefore identical to the first, which is what lets a resumed leg replay at
// the gateway rather than being refused as a different request.
func (g *GatewayReversal) Reverse(ctx context.Context, request ReverseRequest) (ReverseResult, error) {
	if g.funding == nil || g.gateway == nil {
		return ReverseResult{}, fmt.Errorf("reversal path is unavailable")
	}
	if strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.OrderID) == "" {
		return ReverseResult{}, fmt.Errorf("account id and order id are required")
	}
	if request.AmountPaise <= 0 {
		return ReverseResult{}, fmt.Errorf("a reversal needs a positive amount")
	}
	payments, err := g.funding.FundingPayments(ctx, request.AccountID)
	if err != nil {
		return ReverseResult{}, err
	}

	result, err := g.landed(ctx, payments, request.OrderID)
	if err != nil {
		return ReverseResult{}, err
	}
	outstanding := request.AmountPaise - result.ReversedPaise
	for _, funding := range payments {
		if outstanding <= 0 {
			break
		}
		if strings.TrimSpace(funding.PaymentID) == "" {
			continue
		}
		payment, err := g.gateway.Payment(ctx, funding.PaymentID)
		if err != nil {
			return result, err
		}
		available := payment.Refundable()
		if available <= 0 {
			continue
		}
		take := outstanding
		if available < take {
			take = available
		}
		refund, err := g.gateway.CreateRefund(ctx, funding.PaymentID, take, request.IdempotencyKey, g.notes(request, take))
		if err != nil {
			return result, err
		}
		result.RefundIDs = append(result.RefundIDs, refund.ID)
		result.ReversedPaise += take
		outstanding -= take
	}
	if outstanding > 0 {
		result.ShortfallPaise = outstanding
	}
	return result, nil
}

// landed reports what the gateway already holds against this order, across every
// payment that funded the allowance. The gateway is asked rather than our own
// records because a stop between it accepting a leg and us writing that down would
// leave any count of ours short, and a short count here sends the money twice.
// The whole set is totalled before a single leg is sent, since a landed leg may
// sit on a later payment than the one a partial total would size against. A
// reversal it refused and failed to complete moved no money, so it is not counted.
func (g *GatewayReversal) landed(ctx context.Context, payments []FundingPayment, orderID string) (ReverseResult, error) {
	result := ReverseResult{}
	for _, funding := range payments {
		if strings.TrimSpace(funding.PaymentID) == "" {
			continue
		}
		refunds, err := g.gateway.PaymentRefunds(ctx, funding.PaymentID)
		if err != nil {
			return ReverseResult{}, err
		}
		for _, refund := range refunds {
			if refund.Failed() || refund.Notes["order_id"] != orderID {
				continue
			}
			result.RefundIDs = append(result.RefundIDs, refund.ID)
			result.ReversedPaise += refund.Amount
		}
	}
	return result, nil
}

// notes puts the cancelled order and the conversation on the reversal itself, so
// the gateway record explains why the money went back. The gateway hashes this as
// part of the request body, so every value here has to come out the same on a
// resumed leg: that is why the amount in the request is the whole credited sum and
// not what is left of it, and why the run and the reason are the ones the first
// attempt used rather than the ones in front of us.
func (g *GatewayReversal) notes(request ReverseRequest, takePaise int64) map[string]string {
	notes := map[string]string{"order_id": request.OrderID, "account_id": request.AccountID}
	if reason := strings.TrimSpace(request.Reason); reason != "" {
		notes["reason"] = reason
	}
	if run := strings.TrimSpace(request.RunID); run != "" {
		notes["run_id"] = run
	}
	if takePaise != request.AmountPaise {
		notes["part_of_paise"] = fmt.Sprintf("%d", request.AmountPaise)
	}
	return notes
}
