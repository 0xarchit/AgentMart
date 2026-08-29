// Package health probes each layer the agent depends on and reports which ones
// answer. A failing run should never leave a person guessing whether the model
// provider, the catalog tools, the merchant conversation, or the database is
// the one that is down.
package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentmart/internal/failure"
)

// Probe is one layer worth checking, with the cheapest call that proves it
// answers.
type Probe struct {
	Name  string
	Layer failure.Layer
	Check func(context.Context) error
}

// Result is what one probe found.
type Result struct {
	Name    string
	Layer   failure.Layer
	OK      bool
	Latency time.Duration
	Detail  string
}

// probeTimeout bounds a single check so one dead layer cannot stall the report.
const probeTimeout = 30 * time.Second

// Run checks every probe in order and never returns an error: a failed probe is
// a result, not a failure of the report.
func Run(parent context.Context, probes []Probe) []Result {
	results := make([]Result, 0, len(probes))
	for _, probe := range probes {
		if probe.Check == nil {
			results = append(results, Result{Name: probe.Name, Layer: probe.Layer, Detail: "not configured"})
			continue
		}
		ctx, cancel := context.WithTimeout(parent, probeTimeout)
		started := time.Now()
		err := probe.Check(ctx)
		latency := time.Since(started)
		cancel()

		result := Result{Name: probe.Name, Layer: probe.Layer, OK: err == nil, Latency: latency}
		if err != nil {
			result.Detail = failure.Explain(failure.In(probe.Layer, err))
		}
		results = append(results, result)
	}
	return results
}

// Format renders a report a person can read in a chat message.
func Format(results []Result) string {
	if len(results) == 0 {
		return "Nothing to check."
	}
	var report strings.Builder
	report.WriteString("Layer check\n")
	failed := 0
	for _, result := range results {
		if result.OK {
			fmt.Fprintf(&report, "\nok: %s (%dms)", result.Name, result.Latency.Milliseconds())
			continue
		}
		failed++
		fmt.Fprintf(&report, "\ndown: %s\n%s", result.Name, result.Detail)
	}
	if failed == 0 {
		report.WriteString("\n\nEvery layer answered. A shopping request should run end to end.")
	} else {
		fmt.Fprintf(&report, "\n\n%d of %d layers are down. Fix those before shopping.", failed, len(results))
	}
	return report.String()
}
