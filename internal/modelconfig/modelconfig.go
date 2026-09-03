// Package modelconfig reads which model a process thinks with. It holds no
// decision of its own: what the graphs do with these values is their business.
package modelconfig

import (
	"os"
	"strings"
)

// Config controls the OpenAI-compatible reasoning model for one process.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// FromEnv loads the model configuration for one process. Each side of the
// conversation may hold its own key, so a shared pool's per-key rate limit is
// not spent twice on one shopping run. Role is MARKET or USER; when no key is
// set for the role, the shared key is used.
func FromEnv(role string) Config {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY_" + role))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	return Config{
		APIKey:  key,
		BaseURL: strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		Model:   strings.TrimSpace(os.Getenv("ADK_MODEL_NAME")),
	}
}
