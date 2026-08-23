// The offset store tests cover restart-safe Telegram polling checkpoints.
package main

import (
	"context"
	"testing"
)

func TestOffsetStoreRoundTrip(t *testing.T) {
	store := newOffsetStore(t.TempDir() + "/telegram/offset.json")
	if got, err := store.Load(context.Background()); err != nil || got != 0 {
		t.Fatalf("initial load = %d, %v", got, err)
	}
	if err := store.Save(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(context.Background()); err != nil || got != 42 {
		t.Fatalf("saved load = %d, %v", got, err)
	}
}
