// The offset store checkpoints the last Telegram update for restart-safe polling.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type offsetStore struct {
	path string
}

func newOffsetStore(path string) offsetStore {
	if path == "" {
		path = "./data/telegram-offset.json"
	}
	return offsetStore{path: path}
}

func (s offsetStore) Load() (int, error) {
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

func (s offsetStore) Save(offset int) error {
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
