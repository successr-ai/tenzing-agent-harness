package turnqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// gatedRunner blocks each turn until released, recording the query it saw.
type gatedRunner struct {
	mu      sync.Mutex
	queries []string
	gate    chan struct{}
	err     error // returned instead of an answer when set
	panics  bool
}

func (r *gatedRunner) RunTurnWithImages(ctx context.Context, query string, _ []common.ImageSource) (string, error) {
	r.mu.Lock()
	r.queries = append(r.queries, query)
	r.mu.Unlock()
	if r.panics {
		panic("boom")
	}
	select {
	case <-r.gate:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if r.err != nil {
		return "", r.err
	}
	return "ok:" + query, nil
}

func (r *gatedRunner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
}

// recorder captures published events in order.
type recorder struct {
	mu     sync.Mutex
	events []string
	last   map[string]any
}

func (r *recorder) Publish(event string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if r.last == nil {
		r.last = map[string]any{}
	}
	r.last[event] = v
}

// get returns the last payload published under event.
func (r *recorder) get(event string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[event]
}

// all returns the event names in publish order.
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

func newQueue(t *testing.T, runner *gatedRunner, onEnd func()) (*Queue, *recorder) {
	t.Helper()
	rec := &recorder{}
	q := New(QueueConfig{Runner: runner, Events: rec, OnTurnEnd: onEnd})
	t.Cleanup(q.Close)
	return q, rec
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func idle(q *Queue) func() bool {
	return func() bool {
		running, queued := q.State()
		return !running && queued == 0
	}
}

func TestSubmitQueuesWhileBusyAndDrainsInOrder(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{})}
	var ends atomic.Int32
	q, rec := newQueue(t, runner, func() { ends.Add(1) })

	if got := q.Submit(Request{Query: "q1"}); got != Started {
		t.Fatalf("first status = %q, want started", got)
	}
	waitFor(t, "q1 to reach the runner", func() bool { return len(runner.seen()) == 1 })
	if got := q.Submit(Request{Query: "q2"}); got != Queued {
		t.Fatalf("second status = %q, want queued", got)
	}
	if got := q.Submit(Request{Query: "q3"}); got != Queued {
		t.Fatalf("third status = %q, want queued", got)
	}
	if running, queued := q.State(); !running || queued != 2 {
		t.Fatalf("state = (%v, %d), want (true, 2)", running, queued)
	}
	if pos := rec.get("queued").(map[string]any)["position"]; pos != 2 {
		t.Errorf("last queued position = %v, want 2", pos)
	}

	runner.gate <- struct{}{}
	waitFor(t, "q2 to start", func() bool { return len(runner.seen()) == 2 })
	runner.gate <- struct{}{}
	waitFor(t, "q3 to start", func() bool { return len(runner.seen()) == 3 })
	runner.gate <- struct{}{}
	waitFor(t, "queue idle", idle(q))

	if got := runner.seen(); got[0] != "q1" || got[1] != "q2" || got[2] != "q3" {
		t.Fatalf("order = %v", got)
	}
	waitFor(t, "OnTurnEnd", func() bool { return ends.Load() == 1 })
	if rec.count("answer") != 3 || rec.count("status") != 4 { // 3 running + 1 idle
		t.Errorf("events = %v", rec.all())
	}
}

func TestSubmitZeroRequest(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{})}
	q, _ := newQueue(t, runner, nil)
	if got := q.Submit(Request{}); got != Started {
		t.Fatalf("status = %q", got)
	}
	waitFor(t, "turn", func() bool { return len(runner.seen()) == 1 })
	runner.gate <- struct{}{}
	waitFor(t, "idle with nil OnTurnEnd", idle(q))
}

func TestStartNeverQueues(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{})}
	q, _ := newQueue(t, runner, nil)
	if !q.Start("a") {
		t.Fatal("start on idle queue should succeed")
	}
	waitFor(t, "a to reach the runner", func() bool { return len(runner.seen()) == 1 })
	if q.Start("b") {
		t.Fatal("start while busy should refuse")
	}
	if _, queued := q.State(); queued != 0 {
		t.Fatalf("start queued a request: %d", queued)
	}
	runner.gate <- struct{}{}
	waitFor(t, "idle", idle(q))
	if got := runner.seen(); len(got) != 1 {
		t.Fatalf("runner saw %v, want only a", got)
	}
}

func TestCancelDropsQueueAndReportsCanceled(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{})}
	q, rec := newQueue(t, runner, nil)

	if _, ok := q.Cancel(); ok {
		t.Fatal("cancel on idle queue should report nothing running")
	}
	q.Submit(Request{Query: "q1"})
	waitFor(t, "q1", func() bool { return len(runner.seen()) == 1 })
	q.Submit(Request{Query: "q2"})

	dropped, ok := q.Cancel()
	if !ok || dropped != 1 {
		t.Fatalf("cancel = (%d, %v), want (1, true)", dropped, ok)
	}
	waitFor(t, "idle after cancel", idle(q))
	if len(runner.seen()) != 1 {
		t.Fatal("q2 ran despite the cancel")
	}
	if rec.count("canceled") != 1 || rec.count("error") != 0 {
		t.Errorf("events = %v", rec.all())
	}
}

func TestCloseRejectsAndIsIdempotent(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{})}
	q, _ := newQueue(t, runner, nil)
	q.Submit(Request{Query: "q1"})
	waitFor(t, "q1", func() bool { return len(runner.seen()) == 1 })
	q.Submit(Request{Query: "q2"})

	q.Close()
	q.Close()
	waitFor(t, "idle after close", idle(q))
	if q.Submit(Request{Query: "q3"}) != Rejected {
		t.Error("submit after close should be rejected")
	}
	if q.Start("q4") {
		t.Error("start after close should refuse")
	}
	if len(runner.seen()) != 1 {
		t.Error("queued request ran after close")
	}
}

func TestRunnerErrorIsReported(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{}), err: errors.New("model exploded")}
	q, rec := newQueue(t, runner, nil)
	q.Submit(Request{Query: "q"})
	waitFor(t, "q", func() bool { return len(runner.seen()) == 1 })
	runner.gate <- struct{}{}
	waitFor(t, "idle", idle(q))
	if rec.count("error") != 1 || rec.count("answer") != 0 {
		t.Errorf("events = %v", rec.all())
	}
	if got := rec.get("error").(map[string]string)["error"]; got != "model exploded" {
		t.Errorf("error payload = %q", got)
	}
}

func TestRunnerPanicIsReportedAndSlotReleased(t *testing.T) {
	runner := &gatedRunner{gate: make(chan struct{}), panics: true}
	q, rec := newQueue(t, runner, nil)
	q.Submit(Request{Query: "q"})
	waitFor(t, "idle after panic", idle(q))
	if rec.count("error") != 1 {
		t.Errorf("events = %v", rec.all())
	}
	if got := rec.get("error").(map[string]string)["error"]; got != "panic: boom" {
		t.Errorf("error payload = %q", got)
	}
	// the busy slot is free again
	runner.panics = false
	if q.Submit(Request{Query: "again"}) != Started {
		t.Error("queue stuck busy after a panic")
	}
	waitFor(t, "again", func() bool { return len(runner.seen()) == 2 })
	runner.gate <- struct{}{}
}
