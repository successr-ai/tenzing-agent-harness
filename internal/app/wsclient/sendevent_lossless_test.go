package wsclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestSendEventBlocksForLossless pins the backpressure contract
// (PROTOCOL.md §4): SendEvent blocks when the outbox is full rather than
// dropping, and unblocks when the connection context ends — so a future
// "optimization" to non-blocking sends cannot silently break losslessness.
// No double needed: SendEvent's contract is pure channel semantics over the
// client's state, so the test drives it directly.
func TestSendEventBlocksForLossless(t *testing.T) {
	h, _ := handlersFor(t)
	c, err := New(Options{URL: "ws://unused", Backoff: time.Millisecond}, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Install a live turn + connection context, as the wiring would see
	// mid-turn.
	connCtx, connCancel := context.WithCancel(context.Background())
	c.connCtx.set(connCtx)
	c.mu.Lock()
	c.currentID = "q1"
	c.mu.Unlock()

	// Fill the outbox to capacity with the connection "stuck" (nobody
	// draining: no write pump in this direct setup).
	for i := 0; i < cap(c.outbox); i++ {
		select {
		case c.outbox <- []byte("filler"):
		default:
			t.Fatalf("outbox filled only %d of %d", i, cap(c.outbox))
		}
	}

	// SendEvent must now BLOCK, not drop.
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		c.SendEvent(connCtx, "q1", json.RawMessage(`{"v":1}`))
	}()

	select {
	case <-blocked:
		t.Fatal("SendEvent completed despite a full outbox; it dropped events — lossless contract violated")
	case <-time.After(150 * time.Millisecond):
		// Still blocked: correct.
	}

	// Fire the connection context (as a disconnect would): the sender
	// unblocks via ctx without delivering.
	connCancel()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("SendEvent still blocked after the connection context ended")
	}
}
