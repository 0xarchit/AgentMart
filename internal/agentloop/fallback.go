// The deterministic path: same tools, zero model. This is what runs when the
// LLM is unconfigured, unreachable, or blew its budget — the demo cannot die
// on venue Wi-Fi.
package agentloop

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"agentmart/internal/catalog"
	"agentmart/internal/negotiationclient"
)

// stopWords are stripped before the remainder becomes a catalog query.
var stopWords = map[string]bool{
	"buy": true, "me": true, "a": true, "an": true, "the": true, "please": true,
	"under": true, "below": true, "less": true, "than": true, "around": true,
	"about": true, "for": true, "get": true, "want": true, "need": true,
	"best": true, "cheapest": true, "one": true, "unit": true, "units": true,
	"rupees": true, "paise": true, "inr": true, "and": true, "with": true,
}

func fallbackRun(ctx context.Context, tools Tools, state *runState) {
	state.step("deterministic path")
	keywords := nonStopWords(stripBudgetClause(state.request))
	if len(keywords) == 0 {
		state.rationale = "could not work out what product you want; name a product or category"
		return
	}
	ceiling := state.wallet.BudgetPaise
	if ceiling <= 0 {
		ceiling = state.wallet.SpendLimitPaise
	}

	// Search ladder: all keywords together, then each keyword alone. Never
	// widen to the whole catalog — buying something the user did not ask for
	// is worse than declining.
	var candidates []catalog.Product
	queries := append([]string{strings.Join(keywords, " ")}, keywords...)
	for _, query := range queries {
		found, err := tools.Search(ctx, query, ceiling)
		if err != nil {
			state.rationale = fmt.Sprintf("catalog search failed: %v", err)
			return
		}
		if len(found) > 0 {
			candidates = found
			break
		}
	}
	if len(candidates) == 0 {
		state.rationale = fmt.Sprintf("no products matching %q within INR %.2f", strings.Join(keywords, " "), float64(ceiling)/100)
		return
	}

	type candidate struct {
		id    string
		price int64
		name  string
	}
	var inStock []candidate
	for _, product := range candidates {
		if product.Stock > 0 && product.PricePaise*int64(state.quantity) <= ceiling {
			inStock = append(inStock, candidate{product.ID, product.PricePaise, product.Name})
		}
	}
	if len(inStock) == 0 {
		state.rationale = fmt.Sprintf("nothing in stock within INR %.2f for %q", float64(ceiling)/100, strings.Join(keywords, " "))
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
	state.transcript = append(state.transcript, proposal.Transcript...)

	final := proposal.FinalAmountPaise
	base := proposal.BaseAmountPaise
	if base <= 0 {
		base = cheapest.price * int64(state.quantity)
	}

	// The offer is a live A2A session: it must END as accepted or declined
	// before we return, whatever the band says about buying.
	acceptable := func(amount int64) bool {
		premium := amount - base
		if amount > state.wallet.BalancePaise {
			return false
		}
		if state.wallet.BudgetPaise > 0 && amount > state.wallet.BudgetPaise {
			return false
		}
		if base > 0 && premium > 0 && premium*100 > base*AutoBuyPremiumMaxPct {
			return false
		}
		return true
	}
	closeAccepted := func() bool {
		resolution, err := tools.Accept(ctx, proposal.SessionID)
		if err != nil {
			state.rationale = fmt.Sprintf("A2A accept failed: %v", err)
			return false
		}
		state.transcript = append(state.transcript, resolution.Transcript...)
		state.accepted = true
		state.finalPaise = resolution.FinalAmountPaise
		state.step(fmt.Sprintf("accepted at INR %.2f", float64(resolution.FinalAmountPaise)/100))
		return true
	}

	if acceptable(final) {
		if !closeAccepted() {
			return
		}
	} else {
		// One counter at 85% of the ask; then accept whatever still fits the
		// wallet/budget/band, otherwise formally decline the session.
		target := final * 85 / 100
		resolution, cerr := tools.Counter(ctx, proposal.SessionID, target)
		if cerr != nil {
			state.rationale = fmt.Sprintf("A2A counter failed: %v", cerr)
			state.human = &humanRequest{Reason: state.rationale, AmountPaise: final, SessionID: proposal.SessionID}
			return
		}
		state.transcript = append(state.transcript, resolution.Transcript...)
		switch resolution.Status {
		case "accepted":
			state.accepted = true
			final = resolution.FinalAmountPaise
		case "countered":
			final = resolution.FinalAmountPaise
			if acceptable(final) && closeAccepted() {
				break
			}
			if declineResolution, derr := declineSession(ctx, tools, proposal.SessionID, "over my budget after your counter"); derr == nil {
				state.transcript = append(state.transcript, declineResolution.Transcript...)
			}
			state.finalPaise = final
			state.rationale = fmt.Sprintf("merchant floor INR %.2f exceeds my budget or comfort band", float64(final)/100)
			state.human = &humanRequest{Reason: state.rationale, AmountPaise: final, SessionID: proposal.SessionID}
			return
		default:
			state.rationale = "merchant declined the negotiation"
			return
		}
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

// declineSession formally closes an A2A session as rejected by the buyer so
// the merchant side never holds a dangling countered offer.
func declineSession(ctx context.Context, tools Tools, sessionID, reason string) (negotiationclient.Resolution, error) {
	return tools.Decline(ctx, sessionID, reason)
}
