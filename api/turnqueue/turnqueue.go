// Package turnqueue runs agent turns one at a time, queuing follow-up
// requests and draining them in order.
package turnqueue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Runner executes one agent turn. *harness.Harness satisfies it.
type Runner interface {
	// RunTurnWithImages runs a turn for query with optional images attached
	// and returns the final answer.
	RunTurnWithImages(ctx context.Context, query string, images []common.ImageSource) (string, error)
}

// Publisher receives the queue's lifecycle events: `status` ({state,
// query}), `queued` ({query, position}), `answer` ({text}), `error`
// ({error}), and `canceled` ({query}).
type Publisher interface {
	// Publish emits v under the named event.
	Publish(event string, v any)
}

// QueueConfig is everything a Queue needs, injected at construction.
type QueueConfig struct {
	// Runner executes turns. Required.
	Runner Runner
	// Events receives lifecycle events. Required.
	Events Publisher
	// OnTurnEnd is called each time the queue goes idle (no turn running,
	// nothing queued). Optional.
	OnTurnEnd func()
}

// Request is one turn's input.
type Request struct {
	// Query is the user prompt.
	Query string
	// Images are attachments for vision-capable models; may be nil.
	Images []common.ImageSource
}

// Status is the outcome of Submit.
type Status string

// Submit outcomes.
const (
	// Started means the turn began immediately.
	Started Status = "started"
	// Queued means a turn was already running; the request runs after it.
	Queued Status = "queued"
	// Rejected means the queue is closed.
	Rejected Status = "rejected"
)

// Queue runs turns one at a time. While a turn runs, Submit queues
// follow-ups FIFO and the queue chains straight into the next one when a
// turn ends; Start never queues. Cancel stops the running turn and drops
// the queue; Close does the same and rejects further work.
type Queue struct {
	cfg QueueConfig

	mu       sync.Mutex
	cancelFn context.CancelFunc // non-nil while a turn is running (the busy slot)
	closing  bool
	queue    []Request
}

// New builds an idle queue.
func New(cfg QueueConfig) *Queue {
	return &Queue{cfg: cfg}
}

// Submit starts req immediately, or queues it when a turn is already
// running. Returns Started, Queued, or Rejected (closed).
func (q *Queue) Submit(req Request) Status {
	q.mu.Lock()
	if q.closing {
		q.mu.Unlock()
		return Rejected
	}
	if q.cancelFn != nil {
		q.queue = append(q.queue, req)
		pos := len(q.queue)
		q.mu.Unlock()
		q.cfg.Events.Publish("queued", map[string]any{"query": req.Query, "position": pos})
		return Queued
	}
	ctx, cancel := q.claim()
	q.mu.Unlock()
	q.runTurn(ctx, cancel, req)
	return Started
}

// Start begins a turn for query only if the queue is idle. Returns false
// when a turn is running or the queue is closed. Used by callers that must
// not queue (nexus wakes keep their channels pending and retry later).
func (q *Queue) Start(query string) bool {
	q.mu.Lock()
	if q.closing || q.cancelFn != nil {
		q.mu.Unlock()
		return false
	}
	ctx, cancel := q.claim()
	q.mu.Unlock()
	q.runTurn(ctx, cancel, Request{Query: query})
	return true
}

// Cancel stops the running turn and drops every queued request. ok is
// false when nothing was running; dropped is how many queued requests were
// discarded.
func (q *Queue) Cancel() (dropped int, ok bool) {
	q.mu.Lock()
	cancel := q.cancelFn
	dropped = len(q.queue)
	q.queue = nil
	q.mu.Unlock()

	if cancel == nil {
		return dropped, false
	}
	cancel()
	return dropped, true
}

// State reports whether a turn is running and how many requests wait.
func (q *Queue) State() (running bool, queued int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.cancelFn != nil, len(q.queue)
}

// Close cancels the running turn, drops the queue, and rejects further
// Submit/Start calls. Idempotent.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closing = true
	q.queue = nil
	if q.cancelFn != nil {
		q.cancelFn()
	}
	q.mu.Unlock()
}

// claim takes the busy slot and returns the new turn's context. Caller
// holds q.mu.
func (q *Queue) claim() (context.Context, context.CancelFunc) {
	// the turn outlives any HTTP request, so it gets its own context
	ctx, cancel := context.WithCancel(context.Background())
	q.cancelFn = cancel
	return ctx, cancel
}

// runTurn runs one turn in a goroutine. The busy slot must already be
// claimed; finishTurn releases it or chains into the next queued request.
func (q *Queue) runTurn(ctx context.Context, cancel context.CancelFunc, req Request) {
	q.cfg.Events.Publish("status", map[string]string{"state": "running", "query": req.Query})

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("agent panic", "error", rec)
				q.cfg.Events.Publish("error", map[string]string{"error": fmt.Sprintf("panic: %v", rec)})
			}
			cancel()
			q.finishTurn()
		}()
		answer, err := q.cfg.Runner.RunTurnWithImages(ctx, req.Query, req.Images)
		q.report(req, answer, err)
	}()
}

// report publishes a finished turn's outcome.
func (q *Queue) report(req Request, answer string, err error) {
	switch {
	case err == nil:
		q.cfg.Events.Publish("answer", map[string]string{"text": answer})
	case errors.Is(err, context.Canceled):
		// A cancelled turn is the user's own doing — an outcome, not a fault.
		slog.Info("turn canceled", "query", req.Query)
		q.cfg.Events.Publish("canceled", map[string]string{"query": req.Query})
	default:
		q.cfg.Events.Publish("error", map[string]string{"error": err.Error()})
	}
}

// finishTurn releases the busy slot, or keeps it and chains into the next
// queued request so new submissions keep queuing behind it.
func (q *Queue) finishTurn() {
	q.mu.Lock()
	if !q.closing && len(q.queue) > 0 {
		next := q.queue[0]
		q.queue = q.queue[1:]
		ctx, cancel := q.claim()
		q.mu.Unlock()
		q.runTurn(ctx, cancel, next)
		return
	}
	q.cancelFn = nil
	q.mu.Unlock()
	q.cfg.Events.Publish("status", map[string]string{"state": "idle"})
	if q.cfg.OnTurnEnd != nil {
		q.cfg.OnTurnEnd()
	}
}
