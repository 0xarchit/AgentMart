// The reversal side of the money path. Cancelling an order credits the allowance
// internally, and this turns that into a reversal the gateway can confirm, against
// the captured payments that funded the allowance in the first place.
package buyer

import (
	"context"
	"fmt"
	"strings"

	"agentmart/internal/razorpay"
	"agentmart/internal/runid"
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
	CreateRefund(context.Context, string, int64, string, map[string]string) (razorpay.Refund, error)
}

// ReverseRequest is an amount already credited back internally.
type ReverseRequest struct {
	AccountID      string
	OrderID        string
	AmountPaise    int64
	IdempotencyKey string
	Reason         string
}

// ReverseResult reports what the gateway confirmed. A shortfall is stated rather
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

	result := ReverseResult{}
	outstanding := request.AmountPaise
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
		refund, err := g.gateway.CreateRefund(ctx, funding.PaymentID, take, request.IdempotencyKey, g.notes(ctx, request, take))
		if err != nil {
			return result, err
		}
		result.RefundIDs = append(result.RefundIDs, refund.ID)
		result.ReversedPaise += take
		outstanding -= take
	}
	result.ShortfallPaise = outstanding
	return result, nil
}

// notes puts the cancelled order and the conversation on the reversal itself, so
// the gateway record explains why the money went back.
func (g *GatewayReversal) notes(ctx context.Context, request ReverseRequest, takePaise int64) map[string]string {
	notes := map[string]string{"order_id": request.OrderID, "account_id": request.AccountID}
	if reason := strings.TrimSpace(request.Reason); reason != "" {
		notes["reason"] = reason
	}
	if run := runid.From(ctx); run != "" {
		notes["run_id"] = run
	}
	if takePaise != request.AmountPaise {
		notes["part_of_paise"] = fmt.Sprintf("%d", request.AmountPaise)
	}
	return notes
}
