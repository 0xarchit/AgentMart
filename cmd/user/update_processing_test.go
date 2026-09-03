// Tests for restart-safe Telegram update processing.
package main

import (
	"context"
	"errors"
	"testing"

	"agentmart/internal/telegram"
)

type recordingOffsetStore struct {
	offsets []int
	err     error
}

func (s *recordingOffsetStore) Load(context.Context) (int, error) { return 0, nil }

func (s *recordingOffsetStore) Save(_ context.Context, offset int) error {
	if s.err != nil {
		return s.err
	}
	s.offsets = append(s.offsets, offset)
	return nil
}

func TestProcessUpdatesDoesNotAdvanceWhenCheckpointFails(t *testing.T) {
	wantErr := errors.New("checkpoint unavailable")
	store := &recordingOffsetStore{err: wantErr}
	updates := []telegram.Update{{UpdateID: 7, Message: &telegram.Message{Text: "/buy product 1"}}}
	offset, err := processUpdates(t.Context(), updates, 7, store, func(context.Context, *telegram.Message) error {
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if offset != 7 || len(store.offsets) != 0 {
		t.Fatalf("offset = %d, saved = %v", offset, store.offsets)
	}
}

func TestProcessUpdatesAdvancesPastHandlerFailure(t *testing.T) {
	store := &recordingOffsetStore{}
	updates := []telegram.Update{
		{UpdateID: 7, Message: &telegram.Message{Text: "/buy product 1"}},
		{UpdateID: 8, Message: &telegram.Message{Text: "/refund order reason"}},
	}
	wantErr := errors.New("temporary failure")
	processed := 0
	offset, err := processUpdates(t.Context(), updates, 7, store, func(context.Context, *telegram.Message) error {
		processed++
		return wantErr
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if offset != 9 || processed != 2 || len(store.offsets) != 2 {
		t.Fatalf("offset = %d, processed = %d, saved = %v", offset, processed, store.offsets)
	}
}

func TestProcessUpdatesPersistsSuccessfulUpdates(t *testing.T) {
	store := &recordingOffsetStore{}
	updates := []telegram.Update{
		{UpdateID: 7, Message: &telegram.Message{Text: "/buy product 1"}},
		{UpdateID: 8, Message: &telegram.Message{Text: "/refund order reason"}},
	}
	processed := 0
	offset, err := processUpdates(t.Context(), updates, 7, store, func(context.Context, *telegram.Message) error {
		processed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if offset != 9 || processed != 2 || len(store.offsets) != 2 || store.offsets[0] != 8 || store.offsets[1] != 9 {
		t.Fatalf("offset = %d, processed = %d, saved = %v", offset, processed, store.offsets)
	}
}

func TestProcessUpdatesConvertsCallbackToCommand(t *testing.T) {
	store := &recordingOffsetStore{}
	update := telegram.Update{UpdateID: 9, CallbackQuery: &telegram.CallbackQuery{
		ID: "callback", From: telegram.User{ID: 4}, Data: "/approve token",
		Message: &telegram.Message{MessageID: 3, Chat: telegram.Chat{ID: 8}},
	}}
	var got *telegram.Message
	offset, err := processUpdates(t.Context(), []telegram.Update{update}, 9, store, func(_ context.Context, message *telegram.Message) error { got = message; return nil })
	if err != nil || offset != 10 || got == nil || got.Text != "/approve token" || got.From.ID != 4 || got.CallbackQueryID != "callback" {
		t.Fatalf("offset=%d message=%#v err=%v", offset, got, err)
	}
}
