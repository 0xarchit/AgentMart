// Fanning webhook deliveries out to one worker per person, so one person's
// shopping run does not hold up another's. A run can take minutes against a rate
// limited model, and handled in a single loop the second person waited for all of
// it.
package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"agentmart/internal/telegram"
)

const (
	// personQueueDepth is how many messages one person may have waiting while their
	// run is in flight. Past it they are told to wait rather than queued deeper: a
	// person who sends ten messages wants one answer, not ten runs.
	personQueueDepth = 8
	// workerRetireGrace bounds how long shutdown waits for runs in flight.
	workerRetireGrace = 10 * time.Second
)

type messageHandler func(context.Context, *telegram.Message) error

// personWorkers keeps one worker per person. Different people are handled at the
// same time; one person's own messages are handled in arrival order, because that
// person has exactly one worker.
type personWorkers struct {
	mu      sync.Mutex
	queues  map[int64]chan *telegram.Message
	running sync.WaitGroup
	handle  messageHandler
}

func newPersonWorkers(handle messageHandler) *personWorkers {
	return &personWorkers{queues: make(map[int64]chan *telegram.Message), handle: handle}
}

// dispatch queues the message for its person, starting that person's worker if
// there is none. It reports false when that person's queue is already full, which
// the caller answers rather than dropping in silence. It never blocks: one person
// filling their queue must not stall deliveries for everybody else.
func (w *personWorkers) dispatch(ctx context.Context, message *telegram.Message) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	queue, working := w.queues[message.From.ID]
	if !working {
		queue = make(chan *telegram.Message, personQueueDepth)
		w.queues[message.From.ID] = queue
		w.running.Add(1)
		go w.work(ctx, message.From.ID, queue)
	}
	select {
	case queue <- message:
		return true
	default:
		return false
	}
}

// work handles one person's messages until none are left, then retires, so a bot
// with many people does not keep a goroutine per person forever. The retirement
// check holds the same lock dispatch does, and dispatch queues while holding it,
// so a message arriving at that exact moment cannot be left in a queue nobody is
// reading.
func (w *personWorkers) work(ctx context.Context, person int64, queue chan *telegram.Message) {
	defer w.running.Done()
	for {
		select {
		case message := <-queue:
			// A failure is reported to the person by the handler itself, exactly as it
			// was when every message ran in one loop.
			_ = w.handle(ctx, message)
		default:
			w.mu.Lock()
			if len(queue) > 0 {
				w.mu.Unlock()
				continue
			}
			delete(w.queues, person)
			w.mu.Unlock()
			return
		}
	}
}

// wait gives runs in flight a bounded moment to finish at shutdown.
func (w *personWorkers) wait(limit time.Duration) {
	finished := make(chan struct{})
	go func() {
		w.running.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(limit):
	}
}

// active reports how many people have a worker right now.
func (w *personWorkers) active() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queues)
}

// webhookRouter turns the stream of deliveries into work. It is called from one
// goroutine, so the offset it carries needs no lock.
type webhookRouter struct {
	workers     *personWorkers
	checkpoints telegramOffsetStore
	logger      *slog.Logger
	busy        func(context.Context, *telegram.Message)
	offset      int
}

// route handles one delivery: a repeat is dropped, the offset moves on, and the
// message goes to its person's worker.
func (r *webhookRouter) route(ctx context.Context, update telegram.Update) {
	if update.UpdateID < r.offset {
		// Telegram resends a delivery whose answer never reached it. One connection
		// at a time means deliveries arrive in order, so a repeat is always below
		// where we have got to.
		return
	}
	r.offset = update.UpdateID + 1
	if err := r.checkpoints.Save(ctx, r.offset); err != nil {
		// Polling refuses to advance past an unsaved offset, because the update can be
		// fetched again. A delivery cannot: it is already in our hands and Telegram
		// will not send it a second time, so losing the checkpoint must not lose the
		// person's message. A restart then re-reads an older offset, and every
		// purchase key is derived from the message, so the worst case is a repeated
		// reply rather than a repeated charge.
		r.logger.Error("save update offset failed", "error", err, "update_id", update.UpdateID)
	}
	message := messageFrom(update)
	if message == nil {
		return
	}
	if r.workers.dispatch(ctx, message) {
		return
	}
	r.logger.Warn("that person already has a full queue", "telegram_id", message.From.ID)
	if r.busy != nil {
		// In its own goroutine: telling one person to wait must not hold up the next
		// delivery for somebody else.
		go r.busy(ctx, message)
	}
}
