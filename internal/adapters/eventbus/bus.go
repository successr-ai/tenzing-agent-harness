package eventbus

import (
	"log/slog"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

var _ core.Emitter = (*EventBus)(nil)

// EventBus implements core.Emitter and fans out events to buffered subscriber
// channels. Emit is non-blocking: if a subscriber's buffer is full, the
// event is dropped. All methods are safe for concurrent use.
type EventBus struct {
	mu          sync.RWMutex
	subscribers []chan core.Event
	closed      bool
}

// NewEventBus returns a new EventBus with no subscribers.
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Subscribe registers a new subscriber and returns a receive-only channel.
// bufSize controls the channel buffer capacity.
func (b *EventBus) Subscribe(bufSize int) <-chan core.Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan core.Event, bufSize)
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// Unsubscribe removes the subscriber and closes its channel.
func (b *EventBus) Unsubscribe(ch <-chan core.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, sub := range b.subscribers {
		if sub == ch {
			close(sub)
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}

// Emit sends the event to all subscribers. If a subscriber's buffer is full the
// event is dropped and a warning is logged. Emit is a no-op after Close.
func (b *EventBus) Emit(event core.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			slog.Warn("event dropped: subscriber buffer full", "event_type", event.Type())
		}
	}
}

// Close closes all subscriber channels and prevents further Emit calls.
// It is safe to call Close more than once.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for _, ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = nil
}
