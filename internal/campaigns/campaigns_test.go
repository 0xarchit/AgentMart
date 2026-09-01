// Tests for campaign eligibility. A discount is merchant funded money, so the
// cases where nothing is known must resolve to nothing funded rather than to a
// tier that happens to be generous.
package campaigns

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmart/internal/negotiation"
	"agentmart/internal/supabase"
)

// campaignServer answers the eligibility call with a canned body and status.
func campaignServer(t *testing.T, status int, body string, calls *int) *supabase.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			*calls++
		}
		if r.Method != http.MethodPost {
			t.Errorf("eligibility was called with %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := supabase.NewClient(server.URL, "key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAKnownBuyerGetsTheirFundedTier(t *testing.T) {
	provider := NewProvider(campaignServer(t, http.StatusOK,
		`{"tier":"gold","discount_pct":8,"campaign":"spring","orders":6,"spend_paise":420000,"notes":["six orders in the window"]}`, nil))

	tier, pct, notes, err := provider.Eligibility(t.Context(), negotiation.CounterInput{BuyerAccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tier != "gold" || pct != 8 {
		t.Fatalf("tier = %q pct = %d", tier, pct)
	}
	// The campaign that produced the tier is named in the notes, so the trail says
	// which offer applied rather than only that some discount did.
	joined, _ := json.Marshal(notes)
	if len(notes) < 2 {
		t.Fatalf("notes = %s, want the history and the campaign named", joined)
	}
}

func TestAnAnonymousCallerIsNotGivenADiscount(t *testing.T) {
	calls := 0
	provider := NewProvider(campaignServer(t, http.StatusOK, `{}`, &calls))

	tier, pct, notes, err := provider.Eligibility(t.Context(), negotiation.CounterInput{})
	if err != nil {
		t.Fatalf("an anonymous caller is a valid negotiation, not an error: %v", err)
	}
	if tier != "standard" || pct != 0 {
		t.Fatalf("tier = %q pct = %d", tier, pct)
	}
	if len(notes) == 0 {
		t.Fatal("no reason was given for withholding personalisation")
	}
	// Nothing to look up, so nothing should have been asked.
	if calls != 0 {
		t.Fatalf("the database was called %d times for a buyer with no account", calls)
	}
}

func TestALookupFailureFundsNothing(t *testing.T) {
	provider := NewProvider(campaignServer(t, http.StatusInternalServerError, `{"message":"unavailable"}`, nil))

	tier, pct, _, err := provider.Eligibility(t.Context(), negotiation.CounterInput{BuyerAccountID: "account-1"})
	if err == nil {
		t.Fatal("a failed lookup was reported as a successful one")
	}
	// The error is returned, and the values returned beside it are the safe ones,
	// so a caller that logs the error and carries on cannot accidentally discount.
	if tier != "standard" || pct != 0 {
		t.Fatalf("tier = %q pct = %d alongside the error", tier, pct)
	}
}

func TestAnEmptyTierIsNamedRatherThanLeftBlank(t *testing.T) {
	provider := NewProvider(campaignServer(t, http.StatusOK, `{"discount_pct":0}`, nil))

	tier, pct, _, err := provider.Eligibility(t.Context(), negotiation.CounterInput{BuyerAccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tier != "standard" || pct != 0 {
		t.Fatalf("tier = %q pct = %d", tier, pct)
	}
}

func TestAnUnconfiguredProviderSaysSoInsteadOfDiscounting(t *testing.T) {
	if NewProvider(nil) != nil {
		t.Fatal("a provider was built without a database")
	}
	var provider *Provider
	tier, pct, _, err := provider.Eligibility(t.Context(), negotiation.CounterInput{BuyerAccountID: "account-1"})
	if err == nil {
		t.Fatal("an unconfigured provider answered")
	}
	if tier != "standard" || pct != 0 {
		t.Fatalf("tier = %q pct = %d", tier, pct)
	}
}
