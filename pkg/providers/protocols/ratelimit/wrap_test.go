package ratelimit

import (
	"context"
	"errors"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// drainEvents collects all events until the channel closes.
func drainEvents(events <-chan common.StreamEvent) []common.StreamEvent {
	var collected []common.StreamEvent
	for ev := range events {
		collected = append(collected, ev)
	}
	return collected
}

// fakeLimiter records Acquire/Release calls and can fail Acquire.
type fakeLimiter struct {
	acquired   int
	released   int
	lastCost   int64
	acquireErr error
}

func (f *fakeLimiter) Acquire(_ context.Context, cost int64) error {
	if f.acquireErr != nil {
		return f.acquireErr
	}
	f.acquired++
	f.lastCost = cost
	return nil
}

func (f *fakeLimiter) Release() {
	f.released++
}

// fakeLLM is a minimal common.LLM whose behavior each test configures.
type fakeLLM struct {
	sendErr        error
	countTokens    common.TokenCount
	countTokensErr error
	sendCalls      int
}

func (f *fakeLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	f.sendCalls++
	return common.CompletionResponse{ID: "sync"}, f.sendErr
}

func (f *fakeLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, events chan<- common.StreamEvent) error {
	defer close(events)
	f.sendCalls++
	if f.sendErr != nil {
		return f.sendErr
	}
	events <- common.StreamEvent{Type: common.StreamEventStart}
	events <- common.StreamEvent{Type: common.StreamEventStop}
	return nil
}

func (f *fakeLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	f.sendCalls++
	return common.CompletionResponse{ID: "tools"}, f.sendErr
}

func (f *fakeLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return f.countTokens, f.countTokensErr
}

func (f *fakeLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) {
	return []common.ModelInfo{{ID: "m"}}, nil
}

func (f *fakeLLM) GetCurrentModel() string { return "fake-model" }

func (f *fakeLLM) GetContextWindowSize() int { return 4096 }

func (f *fakeLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "fake-model", ContextWindowSize: 4096}
}

func TestWrap_NilLimiterReturnsSameLLM(t *testing.T) {
	inner := &fakeLLM{}
	wrapped := Wrap(inner, nil, nil)
	if wrapped != common.LLM(inner) {
		t.Fatal("Wrap with nil limiter should return the inner LLM unchanged")
	}
}

func TestWrap_AcquireReleasePairing(t *testing.T) {
	tests := []struct {
		name string
		call func(llm common.LLM) error
	}{
		{"SendSyncMessage", func(llm common.LLM) error {
			_, err := llm.SendSyncMessage(context.Background(), common.CompletionRequest{})
			return err
		}},
		{"SendMessageWithTools", func(llm common.LLM) error {
			_, err := llm.SendMessageWithTools(context.Background(), common.CompletionRequest{}, nil)
			return err
		}},
		{"SendStreamingMessage", func(llm common.LLM) error {
			events := make(chan common.StreamEvent, 16)
			return llm.SendStreamingMessage(context.Background(), common.CompletionRequest{}, events)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := &fakeLimiter{}
			wrapped := Wrap(&fakeLLM{}, lim, nil)
			if err := tt.call(wrapped); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if lim.acquired != 1 || lim.released != 1 {
				t.Fatalf("acquired=%d released=%d, want 1/1", lim.acquired, lim.released)
			}
		})
	}
}

func TestWrap_ReleasedOnInnerError(t *testing.T) {
	lim := &fakeLimiter{}
	inner := &fakeLLM{sendErr: errors.New("boom")}
	wrapped := Wrap(inner, lim, nil)

	if _, err := wrapped.SendSyncMessage(context.Background(), common.CompletionRequest{}); err == nil {
		t.Fatal("expected inner error")
	}
	if lim.acquired != 1 || lim.released != 1 {
		t.Fatalf("acquired=%d released=%d, want 1/1", lim.acquired, lim.released)
	}
}

func TestWrap_AcquireFailureBlocksSend(t *testing.T) {
	lim := &fakeLimiter{acquireErr: context.Canceled}
	inner := &fakeLLM{}
	wrapped := Wrap(inner, lim, nil)

	if _, err := wrapped.SendSyncMessage(context.Background(), common.CompletionRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if inner.sendCalls != 0 {
		t.Fatal("inner client must not be called when Acquire fails")
	}
	if lim.released != 0 {
		t.Fatal("Release must not be called when Acquire fails")
	}
}

func TestWrap_StreamingAcquireFailureClosesChannel(t *testing.T) {
	lim := &fakeLimiter{acquireErr: context.Canceled}
	inner := &fakeLLM{}
	wrapped := Wrap(inner, lim, nil)

	events := make(chan common.StreamEvent, 16)
	err := wrapped.SendStreamingMessage(context.Background(), common.CompletionRequest{}, events)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	collected := drainEvents(events)
	if len(collected) != 0 {
		t.Fatalf("want no events, got %d", len(collected))
	}
	if inner.sendCalls != 0 {
		t.Fatal("inner client must not be called when Acquire fails")
	}
}

func TestWrap_StreamingDeliversEvents(t *testing.T) {
	lim := &fakeLimiter{}
	wrapped := Wrap(&fakeLLM{}, lim, nil)

	events := make(chan common.StreamEvent, 16)
	if err := wrapped.SendStreamingMessage(context.Background(), common.CompletionRequest{}, events); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	collected := drainEvents(events)
	if len(collected) != 2 {
		t.Fatalf("want 2 events, got %d", len(collected))
	}
}

func TestWrap_DefaultCostIsOne(t *testing.T) {
	lim := &fakeLimiter{}
	wrapped := Wrap(&fakeLLM{}, lim, nil)

	if _, err := wrapped.SendSyncMessage(context.Background(), common.CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lim.lastCost != 1 {
		t.Fatalf("want cost 1, got %d", lim.lastCost)
	}
}

func TestWrap_CostFuncErrorFailsRequest(t *testing.T) {
	lim := &fakeLimiter{}
	costErr := errors.New("cost failed")
	wrapped := Wrap(&fakeLLM{}, lim, func(context.Context, common.LLM, common.CompletionRequest) (int64, error) {
		return 0, costErr
	})

	if _, err := wrapped.SendSyncMessage(context.Background(), common.CompletionRequest{}); !errors.Is(err, costErr) {
		t.Fatalf("want cost error, got %v", err)
	}
	if lim.acquired != 0 {
		t.Fatal("Acquire must not run when cost func fails")
	}
}

func TestCostByTokenCount(t *testing.T) {
	tests := []struct {
		name     string
		llm      *fakeLLM
		wantCost int64
		wantErr  bool
	}{
		{"uses input tokens", &fakeLLM{countTokens: common.TokenCount{InputTokens: 42}}, 42, false},
		{"falls back to 1 on ErrNotSupported", &fakeLLM{countTokensErr: common.ErrNotSupported}, 1, false},
		{"propagates other errors", &fakeLLM{countTokensErr: errors.New("api down")}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, err := CostByTokenCount(context.Background(), tt.llm, common.CompletionRequest{})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cost != tt.wantCost {
				t.Fatalf("want cost %d, got %d", tt.wantCost, cost)
			}
		})
	}
}

func TestWrap_PassthroughMethodsNotLimited(t *testing.T) {
	lim := &fakeLimiter{}
	wrapped := Wrap(&fakeLLM{countTokens: common.TokenCount{InputTokens: 7}}, lim, nil)

	if _, err := wrapped.CountTokens(context.Background(), common.CompletionRequest{}); err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if _, err := wrapped.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got := wrapped.GetCurrentModel(); got != "fake-model" {
		t.Fatalf("GetCurrentModel = %q", got)
	}
	if got := wrapped.GetContextWindowSize(); got != 4096 {
		t.Fatalf("GetContextWindowSize = %d", got)
	}
	if lim.acquired != 0 || lim.released != 0 {
		t.Fatalf("passthrough methods must not touch limiter: acquired=%d released=%d", lim.acquired, lim.released)
	}
}
