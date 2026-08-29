// Read only view of money the merchant actually received. Every request here is
// a GET: this file has no way to create, capture or refund anything, which is a
// stronger guarantee than a flag that says so.
package razorpay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SalesFacts is what the gateway confirms, as opposed to what our own database
// believes. Amounts are paise.
type SalesFacts struct {
	Since          time.Time
	CapturedCount  int
	CapturedPaise  int64
	RefundedCount  int
	RefundedPaise  int64
	SettledPaise   int64
	IgnoredCount   int
	RefundRatePct  int
	AverageCapture int64
}

type gatewayPayment struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

type gatewayRefund struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

type gatewaySettlement struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

// listLimit is the page size the gateway accepts for these collections.
const listLimit = 100

// SalesFacts reads captured payments, processed refunds and processed
// settlements since a point in time. A payment that was created but never paid
// is counted as ignored rather than as revenue, so unpaid probe artifacts fall
// out on their state and no identifier is ever named.
func (c *Client) SalesFacts(ctx context.Context, since time.Time) (SalesFacts, error) {
	facts := SalesFacts{Since: since}
	query := url.Values{"count": {strconv.Itoa(listLimit)}}
	if !since.IsZero() {
		query.Set("from", strconv.FormatInt(since.Unix(), 10))
	}

	var payments struct {
		Items []gatewayPayment `json:"items"`
	}
	if err := c.list(ctx, "/payments", query, &payments); err != nil {
		return SalesFacts{}, err
	}
	for _, payment := range payments.Items {
		if payment.Status != "captured" {
			facts.IgnoredCount++
			continue
		}
		facts.CapturedCount++
		facts.CapturedPaise += payment.Amount
	}

	var refunds struct {
		Items []gatewayRefund `json:"items"`
	}
	if err := c.list(ctx, "/refunds", query, &refunds); err != nil {
		return SalesFacts{}, err
	}
	for _, refund := range refunds.Items {
		if refund.Status != "processed" {
			continue
		}
		facts.RefundedCount++
		facts.RefundedPaise += refund.Amount
	}

	var settlements struct {
		Items []gatewaySettlement `json:"items"`
	}
	if err := c.list(ctx, "/settlements", query, &settlements); err != nil {
		return SalesFacts{}, err
	}
	for _, settlement := range settlements.Items {
		if settlement.Status != "processed" {
			continue
		}
		facts.SettledPaise += settlement.Amount
	}

	if facts.CapturedCount > 0 {
		facts.AverageCapture = facts.CapturedPaise / int64(facts.CapturedCount)
		facts.RefundRatePct = int(int64(facts.RefundedCount) * 100 / int64(facts.CapturedCount))
	}
	return facts, nil
}

// list performs one authorised GET and decodes the collection into out.
func (c *Client) list(ctx context.Context, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Basic "+c.basicAuth())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}
