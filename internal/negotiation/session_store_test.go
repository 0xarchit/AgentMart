// Tests the durable negotiation session REST contract.
package negotiation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRedisSessionStoreRoundTripUsesTTL(t *testing.T) {
	var commands [][]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var command []any
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
		w.Header().Set("Content-Type", "application/json")
		if command[0] == "GET" {
			encoded, _ := json.Marshal(Session{Proposal: Proposal{ProductID: "p1", Quantity: 1, BaseAmountPaise: 100}, Status: StatusCountered})
			_ = json.NewEncoder(w).Encode(map[string]any{"result": string(encoded)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "OK"})
	}))
	defer server.Close()

	store, err := NewRedisSessionStore(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Proposal: Proposal{ProductID: "p1", Quantity: 1, BaseAmountPaise: 100}, Status: StatusProposed}
	if err := store.Put(context.Background(), "session-1", session); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Get(context.Background(), "session-1")
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if loaded.Proposal.ProductID != "p1" || loaded.Status != StatusCountered {
		t.Fatalf("unexpected session: %+v", loaded)
	}
	if len(commands) != 2 || commands[0][0] != "SET" || commands[0][3] != "EX" || commands[0][4] != float64(int(sessionTTL/time.Second)) {
		t.Fatalf("unexpected Redis commands: %#v", commands)
	}
}

func TestRedisSessionStoreRequiresCredentials(t *testing.T) {
	if _, err := NewRedisSessionStore("", "token", nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected missing URL error, got %v", err)
	}
}

func TestRedisSessionStoreValueDoesNotExpireWhenTTLIsZero(t *testing.T) {
	var command []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "OK"})
	}))
	defer server.Close()
	store, err := NewRedisSessionStore(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutValue(context.Background(), "agentmart:telegram:offset", "42", 0); err != nil {
		t.Fatal(err)
	}
	if len(command) != 3 || command[0] != "SET" || command[1] != "agentmart:telegram:offset" || command[2] != "42" {
		t.Fatalf("command = %#v", command)
	}
}
