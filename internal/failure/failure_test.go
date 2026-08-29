// Tests that a failure names the layer that broke and the check worth making.
package failure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExplainNamesTheLayerAndTheCheck(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantWord string
		wantHint string
	}{
		{
			name:     "the model provider never answered",
			err:      Reasoning(context.DeadlineExceeded),
			wantWord: "reasoning layer",
			wantHint: "model name",
		},
		{
			name:     "nothing is listening for the conversation",
			err:      Conversation(errors.New(`Post "http://localhost:8081/a2a/": connection refused`)),
			wantWord: "merchant conversation channel",
			wantHint: "market process is not running",
		},
		{
			name:     "the catalog tools are wedged",
			err:      Catalog(fmt.Errorf("read tcp: i/o timeout")),
			wantWord: "catalog tool channel",
			wantHint: "market process log",
		},
		{
			name:     "the provider rejected our key",
			err:      Reasoning(errors.New("provider error (401 Unauthorized): invalid api key")),
			wantWord: "reasoning layer",
			wantHint: "API key",
		},
		{
			name:     "the two processes disagree on the token",
			err:      Conversation(errors.New("merchant answered 403 Forbidden")),
			wantWord: "merchant conversation channel",
			wantHint: "shared token",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			explained := Explain(testCase.err)
			if !strings.Contains(explained, testCase.wantWord) {
				t.Fatalf("explained = %q, want it to name %q", explained, testCase.wantWord)
			}
			if !strings.Contains(explained, testCase.wantHint) {
				t.Fatalf("explained = %q, want the hint to mention %q", explained, testCase.wantHint)
			}
		})
	}
}

func TestExplainBelievesTheFarSideAboutItsOwnLayer(t *testing.T) {
	// The merchant's reasoning failed. It reached us over the conversation
	// channel, but the channel is not what broke.
	remote := Conversation(errors.New("reasoning layer: provider error (429 Too Many Requests)"))

	explained := Explain(remote)
	if !strings.Contains(explained, "merchant's reasoning layer") {
		t.Fatalf("explained = %q, want it to blame the merchant's reasoning", explained)
	}
	if !strings.Contains(explained, "rate limiting") {
		t.Fatalf("explained = %q, want the rate limit shape", explained)
	}
}

func TestInKeepsTheFirstLayerAndNilStaysNil(t *testing.T) {
	if In(LayerCatalog, nil) != nil {
		t.Fatal("wrapping no error should stay nil")
	}
	inner := Reasoning(errors.New("provider is down"))
	if layer, _ := LayerOf(Conversation(inner)); layer != LayerReasoning {
		t.Fatalf("layer = %q, want the innermost attribution to win", layer)
	}
}

func TestErrorsIsSurvivesAttribution(t *testing.T) {
	if !errors.Is(Payment(context.Canceled), context.Canceled) {
		t.Fatal("attribution must not hide the cause from errors.Is")
	}
}

func TestExplainWithoutAttributionStillTellsThePersonSomething(t *testing.T) {
	explained := Explain(errors.New("something odd happened"))
	if !strings.Contains(explained, "agent") || !strings.Contains(explained, "process log") {
		t.Fatalf("explained = %q, want a fallback that points at the log", explained)
	}
}
