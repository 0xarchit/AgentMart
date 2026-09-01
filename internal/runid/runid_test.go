// Tests for run correlation. A run identifier that silently disappears would
// scatter one story across unrelated trail rows, so the absent case is asserted
// as carefully as the present one.
package runid

import (
	"testing"
)

func TestARunTravelsOnTheContext(t *testing.T) {
	ctx := With(t.Context(), "run-1")
	if got := From(ctx); got != "run-1" {
		t.Fatalf("run = %q, want run-1", got)
	}
}

func TestWorkOutsideARunSaysSoRatherThanInventingOne(t *testing.T) {
	if got := From(t.Context()); got != "" {
		t.Fatalf("run = %q, want empty outside a run", got)
	}
}

func TestABlankRunIsNotStored(t *testing.T) {
	// Storing a blank would make every row claim membership of a run named
	// nothing, which reads as correlated when it is not.
	ctx := With(t.Context(), "")
	if got := From(ctx); got != "" {
		t.Fatalf("run = %q, want empty", got)
	}
}

func TestAnInnerRunDoesNotLeakOutward(t *testing.T) {
	outer := With(t.Context(), "run-outer")
	inner := With(outer, "run-inner")
	if got := From(inner); got != "run-inner" {
		t.Fatalf("inner run = %q", got)
	}
	if got := From(outer); got != "run-outer" {
		t.Fatalf("outer run = %q, want it untouched", got)
	}
}

func TestEachRunGetsItsOwnIdentifier(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := New()
		if id == "" {
			t.Fatal("a run was given a blank identifier")
		}
		if seen[id] {
			t.Fatalf("identifier %q was handed out twice", id)
		}
		seen[id] = true
	}
}
