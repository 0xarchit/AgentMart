// Tests for the delivery dispatcher: one worker per person, ordered per person,
// a repeat dropped, and a full queue answered rather than dropped in silence.
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agentmart/internal/telegram"
)

func delivery(updateID int, person int64, text string) telegram.Update {
	return telegram.Update{UpdateID: updateID, Message: &telegram.Message{
		MessageID: updateID,
		Chat:      telegram.Chat{ID: person},
		From:      telegram.User{ID: person},
		Text:      text,
	}}
}

// waitFor fails the test rather than hanging when something never happens.
func waitFor(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// The point of the whole change: one person's run must not hold up another's. The
// handler here blocks, so a single worker could only ever report one person inside.
func TestTwoPeopleAreShoppedForAtTheSameTime(t *testing.T) {
	entered := make(chan int64, 2)
	release := make(chan struct{})
	workers := newPersonWorkers(func(_ context.Context, message *telegram.Message) error {
		entered <- message.From.ID
		<-release
		return nil
	})
	ctx := t.Context()
	// Dispatched from separate goroutines, so anything that serialises the handling
	// shows up as a missing person here rather than as a stuck test.
	for _, person := range []int64{11, 22} {
		go func() {
			if !workers.dispatch(ctx, delivery(1, person, "buy a trimmer").Message) {
				t.Errorf("person %d was not queued", person)
			}
		}()
	}
	seen := map[int64]bool{}
	for range 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of 2 people were being handled at once: %v", len(seen), seen)
		}
	}
	close(release)
	workers.wait(3 * time.Second)
	if !seen[11] || !seen[22] {
		t.Fatalf("people handled at once: %v", seen)
	}
}

// The other half of the contract: parallel across people, strictly serial within
// one person. Two runs for the same person at once would read the same wallet
// balance twice and answer the same open decision twice.
func TestOnePersonsMessagesRunInOrderAndNeverOverlap(t *testing.T) {
	var mu sync.Mutex
	inside, overlaps := 0, 0
	completed := make(chan string, 3)
	workers := newPersonWorkers(func(_ context.Context, message *telegram.Message) error {
		mu.Lock()
		inside++
		if inside > 1 {
			overlaps++
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
		completed <- message.Text
		return nil
	})
	for index, text := range []string{"one", "two", "three"} {
		if !workers.dispatch(t.Context(), delivery(index+1, 11, text).Message) {
			t.Fatalf("%q was not queued", text)
		}
	}
	var order []string
	for range 3 {
		select {
		case text := <-completed:
			order = append(order, text)
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of 3 messages finished: %v", len(order), order)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if overlaps != 0 {
		t.Fatalf("one person's messages overlapped %d times", overlaps)
	}
	if got := strings.Join(order, ","); got != "one,two,three" {
		t.Fatalf("handled in order %q, want the order they arrived in", got)
	}
}

// A worker per person must not mean a goroutine per person forever.
func TestAWorkerRetiresWhenIdleAndTakesTheNextMessage(t *testing.T) {
	handled := make(chan int, 2)
	workers := newPersonWorkers(func(_ context.Context, message *telegram.Message) error {
		handled <- message.MessageID
		return nil
	})
	if !workers.dispatch(t.Context(), delivery(1, 11, "one").Message) {
		t.Fatal("the first message was not queued")
	}
	workers.wait(3 * time.Second)
	if left := workers.active(); left != 0 {
		t.Fatalf("%d workers were left running for people with nothing to do", left)
	}
	if !workers.dispatch(t.Context(), delivery(2, 11, "two").Message) {
		t.Fatal("the message after the worker retired was not queued")
	}
	workers.wait(3 * time.Second)
	if len(handled) != 2 {
		t.Fatalf("handled %d of 2 messages: a message that arrived as a worker retired was lost", len(handled))
	}
}

func routerFor(handle messageHandler, store telegramOffsetStore, busy func(context.Context, *telegram.Message)) (*webhookRouter, *personWorkers) {
	workers := newPersonWorkers(handle)
	return &webhookRouter{workers: workers, checkpoints: store, logger: quietLogger(), busy: busy}, workers
}

// Telegram resends a delivery whose answer never reached it. The money layer would
// refuse the repeat on its idempotency key, but the run itself costs model quota
// and would answer the person twice, so it is dropped here.
func TestARepeatedDeliveryIsHandledOnce(t *testing.T) {
	handled := make(chan int, 4)
	store := &recordingOffsetStore{}
	router, workers := routerFor(func(_ context.Context, message *telegram.Message) error {
		handled <- message.MessageID
		return nil
	}, store, nil)
	router.route(t.Context(), delivery(7, 11, "buy a trimmer"))
	router.route(t.Context(), delivery(7, 11, "buy a trimmer"))
	router.route(t.Context(), delivery(8, 11, "and a kettle"))
	workers.wait(3 * time.Second)
	if len(handled) != 2 {
		t.Fatalf("handled %d messages, want 2: the repeat was run again", len(handled))
	}
	if got := store.offsets; len(got) != 2 || got[0] != 8 || got[1] != 9 {
		t.Fatalf("offsets saved = %v, want [8 9]", got)
	}
}

// A tapped button carries the bot's own message, so the person to attribute it to
// is the one who tapped, not the author of the message under the button.
func TestATappedButtonIsRoutedToThePersonWhoTappedIt(t *testing.T) {
	routed := make(chan int64, 1)
	router, workers := routerFor(func(_ context.Context, message *telegram.Message) error {
		routed <- message.From.ID
		return nil
	}, &recordingOffsetStore{}, nil)
	router.route(t.Context(), telegram.Update{UpdateID: 5, CallbackQuery: &telegram.CallbackQuery{
		ID:      "cb-1",
		From:    telegram.User{ID: 22},
		Data:    "/approve token",
		Message: &telegram.Message{Chat: telegram.Chat{ID: 22}, From: telegram.User{ID: 99}, Text: "approve this?"},
	}})
	workers.wait(3 * time.Second)
	select {
	case id := <-routed:
		if id != 22 {
			t.Fatalf("routed to %d, want the person who tapped, 22", id)
		}
	default:
		t.Fatal("the tapped button was never handled")
	}
}

// Polling refuses to advance past an unsaved offset because it can fetch the update
// again. A delivery cannot be fetched again, so it has to be handled anyway.
func TestADeliveryIsHandledEvenWhenItsCheckpointFails(t *testing.T) {
	handled := make(chan int, 1)
	router, workers := routerFor(func(_ context.Context, message *telegram.Message) error {
		handled <- message.MessageID
		return nil
	}, &recordingOffsetStore{err: errors.New("checkpoint unavailable")}, nil)
	router.route(t.Context(), delivery(7, 11, "buy a trimmer"))
	workers.wait(3 * time.Second)
	if len(handled) != 1 {
		t.Fatal("a delivery was dropped because its checkpoint could not be saved, and telegram will not send it again")
	}
}

// One person sending faster than their runs finish must be told, not dropped in
// silence and not allowed to stall the queue for everybody else.
func TestAFullQueueIsAnsweredRatherThanDropped(t *testing.T) {
	var once sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	handled := make(chan int, personQueueDepth+2)
	told := make(chan int64, 4)
	router, workers := routerFor(func(_ context.Context, message *telegram.Message) error {
		once.Do(func() { close(started) })
		<-release
		handled <- message.MessageID
		return nil
	}, &recordingOffsetStore{}, func(_ context.Context, message *telegram.Message) {
		told <- message.From.ID
	})

	router.route(t.Context(), delivery(1, 11, "buy a trimmer"))
	// With the first run held inside the handler, the queue behind it is empty, so
	// exactly personQueueDepth more fit and the next one does not.
	waitFor(t, "the first run to start", started)
	for index := range personQueueDepth {
		router.route(t.Context(), delivery(2+index, 11, "and another"))
	}
	if len(told) != 0 {
		t.Fatalf("a person was told to wait while their queue still had room: %d", len(told))
	}
	router.route(t.Context(), delivery(2+personQueueDepth, 11, "one too many"))

	select {
	case id := <-told:
		if id != 11 {
			t.Fatalf("told person %d to wait, want 11", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a message past the queue depth was dropped without telling the person")
	}
	close(release)
	workers.wait(5 * time.Second)
	if len(handled) != personQueueDepth+1 {
		t.Fatalf("handled %d messages, want %d: the queue lost one it had accepted", len(handled), personQueueDepth+1)
	}
}

// An update with nothing to act on still moves the offset past itself, and must not
// start a worker for a person who said nothing.
func TestAnUpdateWithNothingToActOnStartsNoWorker(t *testing.T) {
	store := &recordingOffsetStore{}
	router, workers := routerFor(func(_ context.Context, message *telegram.Message) error {
		t.Errorf("handled an update with nothing in it: %+v", message)
		return nil
	}, store, nil)
	router.route(t.Context(), telegram.Update{UpdateID: 3})
	router.route(t.Context(), telegram.Update{UpdateID: 4, Message: &telegram.Message{From: telegram.User{ID: 11}, Text: "   "}})
	if left := workers.active(); left != 0 {
		t.Fatalf("%d workers started for updates with nothing to act on", left)
	}
	if got := store.offsets; len(got) != 2 || got[1] != 5 {
		t.Fatalf("offsets saved = %v, want the offset to move past both", got)
	}
}
