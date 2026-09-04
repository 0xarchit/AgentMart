// Tests for run correlation on the trail.
package buyer

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agentmart/internal/failure"
	"agentmart/internal/gate"
	"agentmart/internal/negotiation"
	"agentmart/internal/runid"
	"agentmart/internal/supabase"
)

// trailWatcher returns a store whose inserts are decoded onto the channel, so a
// test can read the row the trail would actually have received.
func trailWatcher(t *testing.T) (*Store, <-chan map[string]any) {
	t.Helper()
	posted := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read insert: %v", err)
			return
		}
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			t.Errorf("decode insert: %v", err)
			return
		}
		posted <- row
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)

	db, err := supabase.NewClient(server.URL, "test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db), posted
}

func TestTrailRowsCarryTheRunTheyCameFrom(t *testing.T) {
	// Correlation is the whole point of the trail: rows that cannot be joined
	// back to the run that caused them explain nothing.
	store, posted := trailWatcher(t)
	decision := gate.Decision{Approved: true, Reason: "within limits"}

	ctx := runid.With(t.Context(), "11111111-1111-1111-1111-111111111111")
	if err := store.RecordGateDecision(ctx, decision); err != nil {
		t.Fatalf("record inside a run: %v", err)
	}
	row := <-posted
	if row["run_id"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("row was written unattached to its run: %v", row["run_id"])
	}

	if err := store.RecordGateDecision(t.Context(), decision); err != nil {
		t.Fatalf("record outside a run: %v", err)
	}
	if _, tagged := (<-posted)["run_id"]; tagged {
		t.Fatal("work outside a run must not invent one")
	}
}

func TestTheRecordedRunKeepsWhatTheTwoSidesSaid(t *testing.T) {
	run := AgentRun{Action: "BUY", ProductID: "product", FinalPaise: 1000}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "transcript") {
		t.Fatal("an empty conversation must not pad the row")
	}

	run.Transcript = []negotiation.Turn{{Actor: "merchant", Message: "Welcome in"}}
	encoded, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "Welcome in") {
		t.Fatalf("the words behind the price were dropped: %s", encoded)
	}
}

// TestTheGateRowSaysWhichWayTheGateWent is the difference between an audit trail
// and a row count. Only the run id was ever asserted here, so swapping the two
// action strings would have recorded every refusal as an approval, and dropping
// the amount would have left a trail that cannot say what was almost spent, with
// the whole suite still green.
func TestTheGateRowSaysWhichWayTheGateWent(t *testing.T) {
	store, posted := trailWatcher(t)
	request := gate.Request{
		AccountID: "account-3", ProductID: "trim-9", Quantity: 2, FinalAmountPaise: 359800,
	}

	if err := store.RecordGateDecision(t.Context(), gate.Decision{
		Approved: true, Reason: "within limits", Request: request,
	}); err != nil {
		t.Fatal(err)
	}
	row := <-posted
	if row["action"] != "gate_approved" {
		t.Fatalf("an approval was recorded as %q", row["action"])
	}
	if row["actor"] != "gate" || row["account_id"] != "account-3" || row["reason"] != "within limits" {
		t.Fatalf("row = %v, want the gate, the account it judged and why", row)
	}
	payload, ok := row["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want the basket the gate judged", row["payload"])
	}
	if payload["product_id"] != "trim-9" {
		t.Fatalf("payload names product %v, want the one judged", payload["product_id"])
	}
	// JSON numbers decode as float64, so compare the value rather than the type.
	if payload["quantity"] != float64(2) || payload["amount_paise"] != float64(359800) {
		t.Fatalf("payload = %v, want the quantity and the amount the gate ruled on", payload)
	}

	// The same request refused has to read as a refusal. This is the assertion a
	// swapped pair of action strings fails.
	if err := store.RecordGateDecision(t.Context(), gate.Decision{
		Approved: false, Reason: "insufficient_wallet_balance", Request: request,
	}); err != nil {
		t.Fatal(err)
	}
	refused := <-posted
	if refused["action"] != "gate_rejected" {
		t.Fatalf("a refusal was recorded as %q", refused["action"])
	}
	if refused["reason"] != "insufficient_wallet_balance" {
		t.Fatalf("the refusal does not say why: %v", refused["reason"])
	}
}

// queryWatcher returns a store whose reads are answered with the given JSON and
// whose request query is put on the channel, so a test can assert what the store
// actually asked the database for. trailWatcher above reads insert bodies and
// never looks at the query string, which is why the account filters below went
// unpinned.
func queryWatcher(t *testing.T, body string) (*Store, <-chan url.Values) {
	t.Helper()
	asked := make(chan url.Values, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	db, err := supabase.NewClient(server.URL, "test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db), asked
}

// TestTheFundingPaymentReadIsScopedToOneAccount pins the filter that decides whose
// card a cancellation refunds. FundingPayments produces the exact list of captured
// payments the reversal spends against, and nothing downstream re-checks ownership:
// the drawdown takes each payment id straight to the gateway, and the landed-leg
// check matches on the order in the notes, not on whose payment it is. Every
// reversal test hands the drawdown a canned slice, so none of them would notice
// this read losing its account.
func TestTheFundingPaymentReadIsScopedToOneAccount(t *testing.T) {
	store, asked := queryWatcher(t, `[{"razorpay_payment_id":"pay_1","amount_paise":50000}]`)

	payments, err := store.FundingPayments(t.Context(), "account-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(payments) != 1 || payments[0].PaymentID != "pay_1" || payments[0].AmountPaise != 50000 {
		t.Fatalf("payments = %+v", payments)
	}
	query := <-asked
	if query.Get("account_id") != "eq.account-3" {
		t.Fatalf("account filter = %q, want eq.account-3. Without it a cancellation refunds whichever account's top-up comes back first.", query.Get("account_id"))
	}
	if query.Get("entry_type") != "eq.topup" || query.Get("razorpay_payment_id") != "not.is.null" {
		t.Fatalf("query = %v, want only captured top-ups, which are the only rows that can be reversed", query)
	}
	// Oldest first, so a reversal drains the money in the order it arrived rather
	// than stranding capacity on whichever payment the database returns first.
	if query.Get("order") != "created_at.asc" {
		t.Fatalf("order = %q, want created_at.asc", query.Get("order"))
	}
}

// TestTheResumedReversalLookupIsScopedToOneAccount pins the filter its own comment
// names: "so a person cannot resume somebody else's refund by naming their order".
// The table is service-role only, so row level security never applies here, and
// the fake used by the refund tests discards the account argument outright, which
// is why no test at that layer could see this filter disappear.
func TestTheResumedReversalLookupIsScopedToOneAccount(t *testing.T) {
	store, asked := queryWatcher(t, `[{"order_id":"order-7","amount_paise":80000,"reason":"cancelled","idempotency_key":"key-1","run_id":"run-9"}]`)

	attempt, found, err := store.OutstandingReversal(t.Context(), "account-3", "order-7")
	if err != nil || !found {
		t.Fatalf("found = %v, err = %v", found, err)
	}
	if attempt.OrderID != "order-7" || attempt.AmountPaise != 80000 || attempt.IdempotencyKey != "key-1" {
		t.Fatalf("attempt = %+v", attempt)
	}
	query := <-asked
	if query.Get("account_id") != "eq.account-3" {
		t.Fatalf("account filter = %q, want eq.account-3. Without it one person's unsettled reversal can be resumed under another's account, which sends their money to the wrong card.", query.Get("account_id"))
	}
	if query.Get("order_id") != "eq.order-7" || query.Get("settled_at") != "is.null" {
		t.Fatalf("query = %v, want the named order and only an unsettled attempt", query)
	}
}

// TestAnUnlinkedIdentityIsToldApartFromAFailedRead pins the sentinel two callers
// depend on. Both used to match on words: the health probe whitelisted "not found"
// and so reported the records layer down on a working system, and the shopping path
// answered every failure with "link your account first", sending people whose
// accounts were already linked off to do the one thing that cannot fix a database.
func TestAnUnlinkedIdentityIsToldApartFromAFailedRead(t *testing.T) {
	// No link row: the identity is genuinely not linked.
	store, _ := queryWatcher(t, `[]`)
	if _, err := store.AccountForTelegram(t.Context(), 0); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked so a caller can tell this from a broken read", err)
	}

	// A refused read is not an unlinked identity, and it names the layer that broke
	// so the person is told what actually happened.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"permission denied"}`, http.StatusForbidden)
	}))
	defer server.Close()
	db, err := supabase.NewClient(server.URL, "test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := NewStore(db).AccountForTelegram(t.Context(), 42)
	if readErr == nil {
		t.Fatal("a refused read came back as a success")
	}
	if errors.Is(readErr, ErrNotLinked) {
		t.Fatalf("a refused read reads as an unlinked identity: %v", readErr)
	}
	if layer, ok := failure.LayerOf(readErr); !ok || layer != failure.LayerRecords {
		t.Fatalf("layer = %v (attributed %v), want the records database named", layer, ok)
	}
}
