// The deterministic path: same tools, zero model. This is what runs when the
// LLM is unconfigured, unreachable, or blew its budget — the demo cannot die
// on venue Wi-Fi.
package agentloop

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// budgetWords are stripped before the remainder becomes a catalog query.
var stopWords = map[string]bool{
	"buy": true, "me": true, "a": true, "an": true, "the": true, "please": true,
	"under": true, "below": true, "less": true, "than": true, "around": true,
	"about": true, "for": true, "get": true, "want": true, "need": true,
	"best": true, "cheapest": true, "one": true, "unit": true, "units": true,
	"rupees": true, "paise": true, "inr": true, "and": true, "with": true,
}

func fallbackRun(ctx context.Context, tools Tools, state *runState) {
	state.step("deterministic path")
	query := stripBudgetClause(state.request)
	if strings.TrimSpace(query) == "" {
		query = strings.Join(nonStopWords(state.request), " ")
	}
	ceiling := state.wallet.BudgetPaise
	if ceiling <= 0 {
		ceiling = state.wallet.SpendLimitPaise
	}

	candidates, err := tools.Search(ctx, query, ceiling)
	if err != nil || len(candidates) == 0 {
		if err == nil { // empty result: widen to full catalog within budget
			candidates, err = tools.Search(ctx, "", ceiling)
		}
		if err != nil {
			state.rationale = fmt.Sprintf("catalog search failed: %v", err)
			return
		}
	}
	var inStock []struct {
		id    string
		price int64
		name  string
	}
	for _, product := range candidates {
		if product.Stock > 0 && product.PricePaise*int64(state.quantity) <= ceiling {
			inStock = append(inStock, struct {
				id    string
				price int64
				name  string
			}{product.ID, product.PricePaise, product.Name})
		}
	}
	if len(inStock) == 0 {
		state.rationale = fmt.Sprintf("nothing in stock within INR %.2f for %q", float64(ceiling)/100, query)
		return
	}
	sort.Slice(inStock, func(i, j int) bool { return inStock[i].price < inStock[j].price })
	cheapest := inStock[0]

	proposal, err := tools.Offers(ctx, cheapest.id, state.quantity)
	if err != nil {
		state.rationale = fmt.Sprintf("merchant quote failed: %v", err)
		return
	}
	state.productID = proposal.ProductID
	state.quantity = proposal.Quantity
	state.sessionID = proposal.SessionID
	state.offers[proposal.SessionID] = proposal

	final := proposal.FinalAmountPaise
	base := proposal.BaseAmountPaise
	if base <= 0 {
		base = cheapest.price * int64(state.quantity)
	}
	premium := final - base
	if premium*100 > base*AutoBuyPremiumMaxPct && premium > 0 {
		// One polite counter at 85% of the ask; take whatever comes back.
		target := proposal.FinalAmountPaise * 85 / 100
		resolution, cerr := tools.Counter(ctx, proposal.SessionID, target)
		if cerr != nil {
			state.finalPaise = final
			state.rationale = "offer above band; counter failed, asking the human"
			state.human = &humanRequest{Reason: state.rationale, AmountPaise: final, SessionID: proposal.SessionID}
			return
		}
		final = resolution.FinalAmountPaise
		state.transcript = append(state.transcript, resolution.Transcript...)
		state.step(fmt.Sprintf("counter -> %s at INR %.2f", resolution.Status, float64(final)/100))
	}
	state.finalPaise = final
	state.rationale = fmt.Sprintf("picked %s at INR %.2f (%s)", proposal.Name, float64(final)/100, proposal.Reason)
}

func stripBudgetClause(request string) string {
	lower := strings.ToLower(request)
	for _, marker := range []string{" under ", " below ", " less than ", " around ", " max "} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			return strings.TrimSpace(request[:idx])
		}
	}
	return request
}

func nonStopWords(request string) []string {
	var kept []string
	for _, word := range strings.Fields(strings.ToLower(request)) {
		cleaned := strings.Trim(word, ".,!?₹0123456789")
		if cleaned != "" && !stopWords[cleaned] {
			kept = append(kept, cleaned)
		}
	}
	return kept
}
