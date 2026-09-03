// Reverses a captured payment at the gateway, so a cancellation leaves evidence
// outside our own tables. Separate from sales.go, which stays structurally
// read-only, and from orders.go, which only ever creates unpaid artifacts.
package razorpay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Refund is a reversal the gateway accepted.
type Refund struct {
	ID        string `json:"id"`
	PaymentID string `json:"payment_id"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
}

// CapturedPayment is the part of a payment this package needs to decide how much
// of it can still be reversed.
type CapturedPayment struct {
	ID             string `json:"id"`
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
	Status         string `json:"status"`
}

// Refundable reports how much of this payment can still be sent back.
func (p CapturedPayment) Refundable() int64 {
	if p.Status != "captured" {
		return 0
	}
	remaining := p.Amount - p.AmountRefunded
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Payment reads one payment, which is the only way to know what an earlier
// reversal already took off it.
func (c *Client) Payment(ctx context.Context, paymentID string) (CapturedPayment, error) {
	if strings.TrimSpace(paymentID) == "" {
		return CapturedPayment{}, fmt.Errorf("payment id is required")
	}
	var payment CapturedPayment
	if err := c.send(ctx, http.MethodGet, "/payments/"+paymentID, nil, &payment); err != nil {
		return CapturedPayment{}, err
	}
	return payment, nil
}

// CreateRefund reverses part or all of a captured payment. The caller decides the
// amount, which is always an amount already reversed internally. The retry key is
// derived here rather than taken as given, because one reversal can span several
// funding payments and the gateway will not accept the same key twice for
// different ones.
func (c *Client) CreateRefund(ctx context.Context, paymentID string, amountPaise int64, key string, notes map[string]string) (Refund, error) {
	if strings.TrimSpace(paymentID) == "" || amountPaise <= 0 {
		return Refund{}, fmt.Errorf("payment id and a positive amount are required")
	}
	body := map[string]any{"amount": amountPaise, "speed": "normal", "notes": notes}
	var refund Refund
	if err := c.send(ctx, http.MethodPost, "/payments/"+paymentID+"/refund", body, &refund, reversalKey(key, paymentID, amountPaise)); err != nil {
		return Refund{}, err
	}
	if refund.ID == "" {
		return Refund{}, fmt.Errorf("reversal returned no id")
	}
	return refund, nil
}

// reversalKey derives the key for one leg of a reversal. The gateway scopes an
// idempotency key to the payment and the body it arrived with, so one internal key
// sent against a second funding payment is refused as a different request under a
// used key, after the first leg has already sent money back. Hashing the payment
// and the amount into the key gives each leg its own, while a genuine retry of the
// same leg reproduces it exactly and lands on the refund already made instead of a
// second one. The digest also satisfies the shape the gateway insists on, at least
// ten characters of letters, digits, hyphens or underscores, which our colon
// separated internal keys do not.
func reversalKey(internal, paymentID string, amountPaise int64) string {
	trimmed := strings.TrimSpace(internal)
	if trimmed == "" {
		// No internal key is a caller asking for no deduplication, so none is
		// invented: a derived one would merge two honest reversals of the same
		// amount from the same payment into a single refund.
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed + "\n" + paymentID + "\n" + fmt.Sprint(amountPaise)))
	return hex.EncodeToString(sum[:16])
}

// send performs one authorised call and decodes the result. An optional
// idempotency key is attached so a retry cannot reverse the same money twice.
func (c *Client) send(ctx context.Context, method, path string, body any, out any, key ...string) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(key) > 0 && strings.TrimSpace(key[0]) != "" {
		req.Header.Set("X-Refund-Idempotency", strings.TrimSpace(key[0]))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
