// The offset store checkpoints the last Telegram update for restart-safe polling.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agentmart/internal/negotiation"
)

type telegramOffsetStore interface {
	Load(context.Context) (int, error)
	Save(context.Context, int) error
}

type offsetStore struct {
	path string
}

func newOffsetStore(path string) offsetStore {
	if path == "" {
		path = "./data/telegram-offset.json"
	}
	return offsetStore{path: path}
}

func (s offsetStore) Load(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read telegram offset: %w", err)
	}
	var value struct {
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, fmt.Errorf("decode telegram offset: %w", err)
	}
	if value.Offset < 0 {
		return 0, fmt.Errorf("telegram offset must not be negative")
	}
	return value.Offset, nil
}

func (s offsetStore) Save(ctx context.Context, offset int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if offset < 0 {
		return fmt.Errorf("telegram offset must not be negative")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create telegram offset directory: %w", err)
	}
	data, err := json.Marshal(struct {
		Offset int `json:"offset"`
	}{Offset: offset})
	if err != nil {
		return fmt.Errorf("encode telegram offset: %w", err)
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write telegram offset: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("commit telegram offset: %w", err)
	}
	return nil
}

type redisOffsetStore struct {
	store *negotiation.RedisSessionStore
}

func (s redisOffsetStore) Load(ctx context.Context) (int, error) {
	value, ok, err := s.store.GetValue(ctx, "agentmart:telegram:offset")
	if err != nil || !ok {
		return 0, err
	}
	var offset int
	if _, err := fmt.Sscanf(value, "%d", &offset); err != nil || offset < 0 {
		return 0, fmt.Errorf("decode telegram Redis offset")
	}
	return offset, nil
}

func (s redisOffsetStore) Save(ctx context.Context, offset int) error {
	if offset < 0 {
		return fmt.Errorf("telegram offset must not be negative")
	}
	return s.store.PutValue(ctx, "agentmart:telegram:offset", fmt.Sprintf("%d", offset), 0)
}
