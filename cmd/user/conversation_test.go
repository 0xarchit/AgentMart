// Tests for per chat conversation memory.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmart/internal/negotiation"
	"agentmart/internal/shopgraph"
)

// fakeRedis answers the two commands conversation memory uses.
func fakeRedis(t *testing.T, stored *string) *negotiation.RedisSessionStore {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "\"SET\"") {
			*stored = string(body)
			_, _ = w.Write([]byte(`{"result":"OK"}`))
			return
		}
		if *stored == "" {
			_, _ = w.Write([]byte(`{"result":null}`))
			return
		}
		var command []any
		_ = json.Unmarshal([]byte(*stored), &command)
		value, _ := json.Marshal(command[2])
		_, _ = w.Write([]byte(`{"result":` + string(value) + `}`))
	}))
	t.Cleanup(server.Close)
	store, err := negotiation.NewRedisSessionStore(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestConversationMemoryRemembersTheShortlist(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	saved := shopgraph.Conversation{
		Brief:   "a trimmer under 3000",
		Options: []shopgraph.PriorOption{{ProductID: "trim-nova", Name: "Nova", PricePaise: 179900}},
		Chosen:  "trim-nova",
	}
	if err := memory.Save(t.Context(), 42, saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Brief != saved.Brief || len(loaded.Options) != 1 || loaded.Options[0].ProductID != "trim-nova" {
		t.Fatalf("loaded = %+v", loaded)
	}
	// One person's memory, so a shortlist cannot surface in someone else's chat.
	if !strings.Contains(stored, "agentmart:chat:42") {
		t.Fatalf("memory is not scoped to the person: %s", stored)
	}
}

func TestUnreadableMemoryIsTreatedAsNoMemory(t *testing.T) {
	stored := `["SET","agentmart:chat:42","not json at all"]`
	memory := redisConversations{store: fakeRedis(t, &stored)}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatalf("unreadable memory should read as empty, not fail: %v", err)
	}
	if !loaded.Empty() {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestNothingRememberedYetIsNotAnError(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Empty() {
		t.Fatalf("a first message started with memory: %+v", loaded)
	}
}

func TestAnEmptyConversationIsWrittenSoAPurchaseIsForgotten(t *testing.T) {
	var stored string
	memory := redisConversations{store: fakeRedis(t, &stored)}
	if err := memory.Save(t.Context(), 42, shopgraph.Conversation{}); err != nil {
		t.Fatal(err)
	}
	// Saving nothing has to reach the store. Skipping the write would leave the
	// bought shortlist in place for the next request to refine.
	if stored == "" {
		t.Fatal("forgetting a finished conversation wrote nothing")
	}
	loaded, err := memory.Load(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Empty() {
		t.Fatalf("a bought shortlist survived: %+v", loaded)
	}
}
