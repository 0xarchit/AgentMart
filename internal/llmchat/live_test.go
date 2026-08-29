// Live provider probe. Skipped unless LIVE_PROVIDER=1, because it spends real
// quota against the endpoint configured in .env. Run it to find out which key,
// model, or payload the provider is actually rejecting.
package llmchat_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentmart/internal/llmchat"
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

// shortlistPayload is the size and shape of fact payload the choosing stage
// sends, which is where live runs fail.
const shortlistPayload = `{"greeting":"welcome in","options":[
{"product_id":"00000000-0000-0000-0000-000000000002","name":"TrimPro Shield","price_paise":240000,"pitch":"our sturdiest build, three year warranty","includes":"CalmSkin Repair Cream at 20 percent off","stock":6,"warranty_years":3,"trust_score":92},
{"product_id":"00000000-0000-0000-0000-000000000001","name":"TrimPro Nova 5-in-1","price_paise":179900,"pitch":"five attachments, good all rounder","stock":12,"warranty_years":2,"trust_score":88},
{"product_id":"00000000-0000-0000-0000-000000000003","name":"TrimPro Basic","price_paise":200000,"pitch":"plain and reliable","stock":20,"warranty_years":1,"trust_score":75},
{"product_id":"00000000-0000-0000-0000-000000000004","name":"TrimPro Lite Everyday","price_paise":129900,"pitch":"light daily upkeep","stock":40,"warranty_years":1,"trust_score":71}],
"closing":"say the word and I will price one up"}`

// selectionSchema mirrors what the choosing stage asks for.
var selectionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"product_id": map[string]any{"type": "string"},
		"quantity":   map[string]any{"type": "integer"},
		"rationale":  map[string]any{"type": "string"},
	},
	"required": []string{"product_id", "quantity", "rationale"},
}

func TestLiveProviderAnswersForEachKey(t *testing.T) {
	if os.Getenv("LIVE_PROVIDER") != "1" {
		t.Skip("set LIVE_PROVIDER=1 to spend real quota")
	}
	env := dotEnv(t)
	baseURL, modelName := env["OPENAI_BASE_URL"], env["ADK_MODEL_NAME"]
	if baseURL == "" || modelName == "" {
		t.Fatalf("OPENAI_BASE_URL and ADK_MODEL_NAME must be set, got %q and %q", baseURL, modelName)
	}
	t.Logf("endpoint %s model %s", baseURL, modelName)

	for _, role := range []string{"MARKET", "USER"} {
		key := env["OPENAI_API_KEY_"+role]
		source := "OPENAI_API_KEY_" + role
		if key == "" {
			key, source = env["OPENAI_API_KEY"], "OPENAI_API_KEY (shared)"
		}
		if key == "" {
			t.Errorf("%s: no key configured", role)
			continue
		}
		t.Run(role, func(t *testing.T) {
			t.Logf("using %s, ending %s", source, tail(key))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			started := time.Now()
			answer, err := llmchat.New(modelName, key, baseURL).CompleteJSON(ctx, llmchat.CompleteRequest{
				System:       "The shop has shown you what it has. Pick exactly one and say why in one line.",
				User:         shortlistPayload,
				FunctionName: "choose_option",
				Description:  "Choose one product from the shortlist.",
				Parameters:   selectionSchema,
			})
			if err != nil {
				t.Fatalf("failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
			}
			t.Logf("answered in %s: %v", time.Since(started).Round(time.Millisecond), answer)
		})
	}
}

// tail shows enough of a key to tell two apart without printing either.
func tail(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
