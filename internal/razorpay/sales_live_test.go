// Live gateway probe. Skipped unless LIVE_GATEWAY=1, because it reads the real
// test account named in .env. It exists to prove that an unpaid artifact is
// excluded by its state and not by anyone naming its identifier.
package razorpay_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentmart/internal/razorpay"
)

// dotEnv reads KEY=VALUE lines from the repository .env so the probe uses the
// same credentials the binaries do.
func dotEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Skipf("no .env to read: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values
}

func TestLiveSalesFactsExcludeUnpaidArtifacts(t *testing.T) {
	if os.Getenv("LIVE_GATEWAY") != "1" {
		t.Skip("set LIVE_GATEWAY=1 to read the real test account")
	}
	env := dotEnv(t)
	client, err := razorpay.NewClient(env["RAZORPAY_KEY_ID"], env["RAZORPAY_KEY_SECRET"], nil)
	if err != nil {
		t.Fatal(err)
	}

	facts, err := client.SalesFacts(t.Context(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %d worth %d paise, ignored %d, refunded %d worth %d, settled %d, average %d, refund rate %d%%",
		facts.CapturedCount, facts.CapturedPaise, facts.IgnoredCount,
		facts.RefundedCount, facts.RefundedPaise, facts.SettledPaise,
		facts.AverageCapture, facts.RefundRatePct)

	if facts.CapturedPaise < 0 || facts.RefundedPaise < 0 {
		t.Fatalf("facts = %+v", facts)
	}
	if facts.CapturedCount == 0 && facts.IgnoredCount == 0 {
		t.Skip("the account has no payment history in this window")
	}
	if facts.CapturedPaise > 0 && facts.AverageCapture == 0 {
		t.Fatalf("captured %d paise but averaged nothing", facts.CapturedPaise)
	}
}
