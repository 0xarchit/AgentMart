// Session storage keeps negotiation state durable across process restarts.
package negotiation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionTTL = 45 * time.Minute

// SessionStore persists negotiation sessions by ID.
type SessionStore interface {
	Get(ctx context.Context, id string) (Session, bool, error)
	Put(ctx context.Context, id string, session Session) error
}

type memorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
}

// NewMemorySessionStore constructs an in-process session store for tests and local demos.
func NewMemorySessionStore() SessionStore {
	return newMemorySessionStore()
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{sessions: make(map[string]Session)}
}

func (s *memorySessionStore) Get(_ context.Context, id string) (Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok, nil
}

func (s *memorySessionStore) Put(_ context.Context, id string, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
	return nil
}

// RedisSessionStore stores sessions through a Redis-compatible REST API.
type RedisSessionStore struct {
	baseURL string
	token   string
	client  *http.Client
	ttl     time.Duration
}

// GetValue reads a namespaced Redis string for small shared runtime state.
func (s *RedisSessionStore) GetValue(ctx context.Context, key string) (string, bool, error) {
	result, err := s.command(ctx, []any{"GET", key})
	if err != nil {
		return "", false, err
	}
	if string(result) == "null" || len(result) == 0 {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(result, &value); err != nil {
		return "", false, fmt.Errorf("decode Redis value: %w", err)
	}
	return value, true, nil
}

// PutValue writes a namespaced Redis string, optionally with a TTL.
func (s *RedisSessionStore) PutValue(ctx context.Context, key, value string, ttl time.Duration) error {
	command := []any{"SET", key, value}
	if ttl > 0 {
		command = append(command, "EX", int(ttl.Seconds()))
	}
	_, err := s.command(ctx, command)
	return err
}

// NewRedisSessionStore creates a REST-backed session store.
func NewRedisSessionStore(baseURL, token string, client *http.Client) (*RedisSessionStore, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("Redis session URL and token are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &RedisSessionStore{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: client, ttl: sessionTTL}, nil
}

type redisResponse struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func (s *RedisSessionStore) command(ctx context.Context, command []any) (json.RawMessage, error) {
	body, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 200 {
			message = message[:200]
		}
		return nil, fmt.Errorf("Redis session store returned status %d: %s", resp.StatusCode, message)
	}
	var result redisResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode Redis session store response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("Redis session store command failed: %s", result.Error)
	}
	return result.Result, nil
}

// Get loads a session and reports false when its TTL has expired.
func (s *RedisSessionStore) Get(ctx context.Context, id string) (Session, bool, error) {
	result, err := s.command(ctx, []any{"GET", sessionKey(id)})
	if err != nil {
		return Session{}, false, err
	}
	if string(result) == "null" || len(result) == 0 {
		return Session{}, false, nil
	}
	var encoded string
	if err := json.Unmarshal(result, &encoded); err != nil {
		return Session{}, false, fmt.Errorf("decode negotiation session value: %w", err)
	}
	var session Session
	if err := json.Unmarshal([]byte(encoded), &session); err != nil {
		return Session{}, false, fmt.Errorf("decode negotiation session: %w", err)
	}
	return session, true, nil
}

// Put stores a session with the configured expiry.
func (s *RedisSessionStore) Put(ctx context.Context, id string, session Session) error {
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.command(ctx, []any{"SET", sessionKey(id), string(encoded), "EX", int(s.ttl.Seconds())})
	return err
}

func sessionKey(id string) string {
	return "agentmart:negotiation:" + id
}
