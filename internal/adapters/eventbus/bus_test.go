package eventbus

import (
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func TestEventBusImplementsEmitter(t *testing.T) {
	var _ core.Emitter = NewEventBus()
}

func TestSubscribeReceivesEmittedEvents(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(10)
	ev := core.LoopStartedEvent{
		BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1"),
		Input:     "hello",
	}
	bus.Emit(ev)

	select {
	case got := <-ch:
		if got.Type() != core.EventLoopStarted {
			t.Errorf("Type() = %q, want %q", got.Type(), core.EventLoopStarted)
		}
		lse, ok := got.(core.LoopStartedEvent)
		if !ok {
			t.Fatalf("expected core.LoopStartedEvent, got %T", got)
		}
		if lse.Input != "hello" {
			t.Errorf("Input = %q, want %q", lse.Input, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch1 := bus.Subscribe(10)
	ch2 := bus.Subscribe(10)

	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})

	for _, ch := range []<-chan core.Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type() != core.EventLoopStarted {
				t.Errorf("Type() = %q, want %q", got.Type(), core.EventLoopStarted)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestEmitDropsWhenBufferFull(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(1)

	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})
	bus.Emit(core.LoopStoppedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStopped, "r1")})

	got := <-ch
	if got.Type() != core.EventLoopStarted {
		t.Errorf("expected first event, got %q", got.Type())
	}

	select {
	case extra := <-ch:
		t.Errorf("expected no second event, got %q", extra.Type())
	default:
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(10)
	bus.Unsubscribe(ch)

	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after Unsubscribe")
		}
	default:
	}
}

func TestCloseClosesAllSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe(10)
	ch2 := bus.Subscribe(10)

	bus.Close()

	for _, ch := range []<-chan core.Event{ch1, ch2} {
		_, ok := <-ch
		if ok {
			t.Error("channel should be closed after Close")
		}
	}
}

func TestEmitAfterCloseIsNoop(t *testing.T) {
	bus := NewEventBus()
	bus.Close()

	// should not panic
	bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})
}

func TestConcurrentEmit(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch := bus.Subscribe(100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit(core.LoopStartedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStarted, "r1")})
		}()
	}
	wg.Wait()

	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 50 {
		t.Errorf("received %d events, want 50", count)
	}
}
