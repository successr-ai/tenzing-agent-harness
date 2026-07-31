package eventbus

import (
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func TestStartHooksDispatchesMatchingEvent(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	var got core.LoopStartedEvent
	var mu sync.Mutex
	hooks := Hooks{
		OnLoopStarted: func(ev core.LoopStartedEvent) {
			mu.Lock()
			got = ev
			mu.Unlock()
		},
	}

	StartHooks(bus, hooks)

	bus.Emit(core.LoopStartedEvent{
		BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1"),
		Input:     "test-input",
	})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if got.Input != "test-input" {
		t.Errorf("Input = %q, want %q", got.Input, "test-input")
	}
}

func TestStartHooksSkipsNilHooks(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	StartHooks(bus, Hooks{})

	// should not panic
	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})
	time.Sleep(50 * time.Millisecond)
}

func TestStartHooksStopHaltsDispatch(t *testing.T) {
	bus := NewEventBus()
	var mu sync.Mutex
	count := 0
	stop := StartHooks(bus, Hooks{
		OnTurnStarted: func(core.TurnStartedEvent) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})

	bus.Emit(core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "")})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 1 })

	stop()
	bus.Emit(core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "")})

	time.Sleep(50 * time.Millisecond) // give a wrong implementation the chance to dispatch
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("hook dispatched after stop: count = %d, want 1", count)
	}
}

func TestStartHooksStopsOnBusClose(t *testing.T) {
	bus := NewEventBus()
	stop := StartHooks(bus, Hooks{})
	bus.Close()
	stop() // must be safe after Close (idempotent unsubscribe of an already-removed channel)
}

// waitFor polls cond until true or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition not met in time")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestStartHooksMultipleEventTypes(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	var mu sync.Mutex
	var loopCalled, toolCalled bool

	hooks := Hooks{
		OnLoopStarted: func(_ core.LoopStartedEvent) {
			mu.Lock()
			loopCalled = true
			mu.Unlock()
		},
		OnToolSucceeded: func(_ core.ToolSucceededEvent) {
			mu.Lock()
			toolCalled = true
			mu.Unlock()
		},
	}

	StartHooks(bus, hooks)

	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})
	bus.Emit(core.ToolSucceededEvent{BaseEvent: core.NewBaseEvent(core.EventToolSucceeded, "r1"), ToolName: "bash"})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !loopCalled {
		t.Error("OnLoopStarted was not called")
	}
	if !toolCalled {
		t.Error("OnToolSucceeded was not called")
	}
}
