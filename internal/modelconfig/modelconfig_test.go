// Tests for the per-process model configuration.
package modelconfig

import "testing"

// The per-role key is what keeps one shopping run from spending a shared pool's
// per-key rate limit twice, so a role key must win over the shared one and a
// missing role key must fall back rather than leaving the process unconfigured.
func TestARoleKeyWinsAndAMissingOneFallsBack(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "shared")
	t.Setenv("OPENAI_BASE_URL", " https://provider.example/v1 ")
	t.Setenv("ADK_MODEL_NAME", "first,second")
	t.Setenv("OPENAI_API_KEY_USER", " user-key ")

	buyer := FromEnv("USER")
	if buyer.APIKey != "user-key" {
		t.Fatalf("APIKey = %q, want the role key trimmed", buyer.APIKey)
	}
	if buyer.BaseURL != "https://provider.example/v1" || buyer.Model != "first,second" {
		t.Fatalf("BaseURL = %q, Model = %q", buyer.BaseURL, buyer.Model)
	}
	if merchant := FromEnv("MARKET"); merchant.APIKey != "shared" {
		t.Fatalf("APIKey = %q, want the shared key when the role has none", merchant.APIKey)
	}
}
