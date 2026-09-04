// The reversal side of the money path. Cancelling an order credits the allowance
// internally, and this records that cancellation at the payment gateway so it can
// be checked outside our own tables.
//
// It moves no money, and that is the decision rather than an omission. The
// allowance is the one channel this product has: money enters it through a
// captured top-up and leaves it as goods, and there is no path anywhere that pays
// a person out. The credit has already put the cancelled amount back where they
// can spend it, and what the person is told, that the refund was applied to their
// wallet, is exactly what happened. Sending the card its money back as well would
// return the same amount twice: the allowance would still be spendable and the
// shop would be short by the price of the cancelled order.
//
// What used to be here refunded the captured top-ups that funded the allowance,
// which is why the drawing down across payments, the refundable capacity of each
// one and the shortfall when they had nothing left to give are all gone. The
// gateway record is what the reversal is for.
package buyer

import (
	"context"
	"fmt"
	"strings"
)

// ReverseRequest is an amount already credited back internally. Every field of it
// ends up in the gateway record, and a repeat of an interrupted reversal has to
// reproduce that record, so the run is carried here with the rest rather than read
// from the surrounding context: the run in front of us is not the run the first
// attempt used.
type ReverseRequest struct {
	AccountID      string
	OrderID        string
	AmountPaise    int64
	IdempotencyKey string
	Reason         string
	RunID          string
}

// ReverseResult reports the gateway record the cancellation left behind.
type ReverseResult struct {
	// RefundIDs are the gateway objects holding this cancellation. They are order
	// ids rather than refund ids, because the object is a record of money returned
	// inside the allowance and not a second payout, and they are a list because the
	// order column they are written to has always been one.
	RefundIDs []string
	// ReversedPaise is what the cancellation returned, which is the whole credited
	// amount. There is no partial case: one record states one cancellation.
	ReversedPaise int64
}

// Reversal turns an internal credit into gateway evidence. A nil implementation
// leaves the allowance path exactly as it was.
type Reversal interface {
	Reverse(context.Context, ReverseRequest) (ReverseResult, error)
}

// GatewayReversal records a cancellation at the gateway, using the same unpaid
// order object every allowance purchase already records itself with.
type GatewayReversal struct {
	artifacts artifactCreator
}

// NewGatewayReversal builds the reversal path.
func NewGatewayReversal(artifacts artifactCreator) *GatewayReversal {
	return &GatewayReversal{artifacts: artifacts}
}

// Reverse records the whole credited amount as one gateway object, with the
// cancelled order, the person's account, the reason and the run on it, so the
// record explains itself without our tables.
//
// ponytail: a stop between the gateway accepting this and the trail write landing
// leaves the attempt unsettled, and the resumed leg records a second object for the
// same cancellation. Both carry the same receipt and notes, and neither moves
// money, so the duplicate is untidy rather than harmful. Asking the gateway whether
// a receipt already exists is the fix if that ever matters.
func (g *GatewayReversal) Reverse(ctx context.Context, request ReverseRequest) (ReverseResult, error) {
	if g.artifacts == nil {
		return ReverseResult{}, fmt.Errorf("reversal path is unavailable")
	}
	if strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.OrderID) == "" {
		return ReverseResult{}, fmt.Errorf("account id and order id are required")
	}
	if request.AmountPaise <= 0 {
		return ReverseResult{}, fmt.Errorf("a reversal needs a positive amount")
	}
	artifact, err := g.artifacts.CreateWalletArtifact(ctx, request.AmountPaise, receiptFor(request), g.notes(request))
	if err != nil {
		return ReverseResult{}, err
	}
	return ReverseResult{RefundIDs: []string{artifact.ID}, ReversedPaise: request.AmountPaise}, nil
}

// receiptFor names the cancellation on the gateway object. It is derived from the
// same key the credit was idempotent on, so the record and the credit can be lined
// up, and it is prefixed rather than reused so a cancellation is never mistaken for
// the purchase it reverses. The gateway caps a receipt at forty characters.
func receiptFor(request ReverseRequest) string {
	receipt := "reversal_" + request.IdempotencyKey
	if len(receipt) > 40 {
		receipt = receipt[:40]
	}
	return receipt
}

// notes puts the cancelled order and the conversation on the record itself, so the
// gateway object explains why the money went back without anything of ours to read
// alongside it.
func (g *GatewayReversal) notes(request ReverseRequest) map[string]string {
	notes := map[string]string{
		"order_id":   request.OrderID,
		"account_id": request.AccountID,
		"returned":   "wallet_allowance",
	}
	if reason := strings.TrimSpace(request.Reason); reason != "" {
		notes["reason"] = reason
	}
	if run := strings.TrimSpace(request.RunID); run != "" {
		notes["run_id"] = run
	}
	return notes
}
