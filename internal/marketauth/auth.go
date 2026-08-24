// Package marketauth protects private merchant service endpoints.
package marketauth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// RequireBearer rejects requests that do not carry the configured token.
func RequireBearer(token string, next http.Handler) (http.Handler, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("market shared token is required")
	}
	if next == nil {
		return nil, fmt.Errorf("protected market handler is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// NewClient returns an HTTP client that attaches the market bearer token.
func NewClient(token string, base *http.Client) (*http.Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("market shared token is required")
	}
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = bearerTransport{token: token, base: transport}
	return &clone, nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
