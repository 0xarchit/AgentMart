// Tests for the layer report. Its whole purpose is to answer which layer is down
// without guessing, so a probe that cannot run must be distinguishable from one
// that ran and passed.
package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentmart/internal/failure"
)

func TestAFailingProbeIsAResultNotAnError(t *testing.T) {
	results := Run(t.Context(), []Probe{
		{Name: "records", Layer: failure.LayerRecords, Check: func(context.Context) error {
			return errors.New("connection refused")
		}},
		{Name: "gate", Layer: failure.LayerGate, Check: func(context.Context) error { return nil }},
	})

	if len(results) != 2 {
		t.Fatalf("got %d results, want one per probe", len(results))
	}
	if results[0].OK || results[0].Detail == "" {
		t.Fatalf("failed probe = %+v, want it marked down with a reason", results[0])
	}
	if !results[1].OK {
		t.Fatalf("passing probe = %+v", results[1])
	}
}

func TestAnUnconfiguredProbeIsNotReportedAsHealthy(t *testing.T) {
	// A layer nobody wired must never read as a layer that answered. That is the
	// difference between "this works" and "nobody asked".
	results := Run(t.Context(), []Probe{{Name: "payments", Layer: failure.LayerPayment}})

	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	if results[0].OK {
		t.Fatal("a probe with nothing to call was reported as healthy")
	}
	if results[0].Detail != "not configured" {
		t.Fatalf("detail = %q, want it to say nobody wired it", results[0].Detail)
	}
}

func TestOneDeadLayerDoesNotStallTheRest(t *testing.T) {
	// The parent's own deadline bounds the check, so a layer that never answers
	// cannot hold the report open. Asserted against a short parent rather than the
	// real timeout, so proving it costs milliseconds instead of half a minute.
	parent, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	results := Run(parent, []Probe{
		{Name: "slow", Layer: failure.LayerReasoning, Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
	})
	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	if results[0].OK {
		t.Fatal("a layer that never answered was reported as healthy")
	}
	if results[0].Latency > 5*time.Second {
		t.Fatalf("probe took %s, want it bounded by the caller's deadline", results[0].Latency)
	}
}

func TestTheReportNamesEveryLayerThatIsDown(t *testing.T) {
	report := Format([]Result{
		{Name: "records", Layer: failure.LayerRecords, OK: true, Latency: 12 * time.Millisecond},
		{Name: "payments", Layer: failure.LayerPayment, Detail: "no credentials"},
	})

	if !strings.Contains(report, "ok: records") {
		t.Fatalf("report does not name the layer that answered:\n%s", report)
	}
	if !strings.Contains(report, "down: payments") || !strings.Contains(report, "no credentials") {
		t.Fatalf("report does not explain the layer that failed:\n%s", report)
	}
	if !strings.Contains(report, "1 of 2 layers are down") {
		t.Fatalf("report does not count the failures:\n%s", report)
	}
}

func TestAnAllClearSaysShoppingShouldWork(t *testing.T) {
	report := Format([]Result{{Name: "gate", Layer: failure.LayerGate, OK: true}})
	if !strings.Contains(report, "Every layer answered") {
		t.Fatalf("report = %q", report)
	}
	if strings.Contains(report, "down:") {
		t.Fatalf("an all clear report named something as down:\n%s", report)
	}
}

func TestAnEmptyReportSaysSoRatherThanReadingAsHealthy(t *testing.T) {
	if report := Format(nil); report != "Nothing to check." {
		t.Fatalf("report = %q", report)
	}
}
