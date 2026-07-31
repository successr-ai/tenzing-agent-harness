package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// flakyLLM fails the first failN SendMessageWithTools calls with err, then
// succeeds with a fixed answer.
type flakyLLM struct {
	failN int
	err   error
	calls int
}

func (f *flakyLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (f *flakyLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, ch chan<- common.StreamEvent) error {
	close(ch)
	return f.err
}
func (f *flakyLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	f.calls++
	if f.calls <= f.failN {
		return common.CompletionResponse{}, f.err
	}
	return common.CompletionResponse{
		Model:   "flaky",
		Content: []common.ContentBlock{common.NewTextContent("recovered")},
	}, nil
}
func (f *flakyLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (f *flakyLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (f *flakyLLM) GetCurrentModel() string                                  { return "flaky" }
func (f *flakyLLM) GetContextWindowSize() int                                { return 4096 }

func (f *flakyLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "flaky-model", ContextWindowSize: 128000, SupportsVision: true}
}

func newRetryAgent(t *testing.T, llm common.LLM, onRetry func(int, int, error, time.Duration)) *Agent {
	t.Helper()
	a, err := New(AgentConfig{
		Model:          llm,
		SystemPrompt:   "test",
		RetryMax:       3,
		RetryBaseDelay: time.Millisecond,
		OnLLMRetry:     onRetry,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func userMsgs(text string) []common.Message {
	return []common.Message{common.NewUserMessage(text)}
}

func TestRetryTransientThenSucceed(t *testing.T) {
	llm := &flakyLLM{failN: 2, err: fmt.Errorf("upstream said 503 service unavailable")}
	var retries int
	a := newRetryAgent(t, llm, func(attempt, max int, err error, d time.Duration) { retries++ })

	res, err := a.DoReasoning(context.Background(), userMsgs("hi"), nil, nil)
	if err != nil {
		t.Fatalf("DoReasoning: %v", err)
	}
	if res.FinalAnswer != "recovered" {
		t.Errorf("answer = %q", res.FinalAnswer)
	}
	if retries != 2 {
		t.Errorf("retries = %d, want 2", retries)
	}
	if llm.calls != 3 {
		t.Errorf("llm calls = %d, want 3", llm.calls)
	}
}

func TestNoRetryOnNonTransient(t *testing.T) {
	llm := &flakyLLM{failN: 10, err: fmt.Errorf("invalid_request: model does not exist (400)")}
	var retries int
	a := newRetryAgent(t, llm, func(int, int, error, time.Duration) { retries++ })

	if _, err := a.DoReasoning(context.Background(), userMsgs("hi"), nil, nil); err == nil {
		t.Fatal("expected error")
	}
	if retries != 0 || llm.calls != 1 {
		t.Errorf("retries=%d calls=%d, want 0/1 (400 is not transient)", retries, llm.calls)
	}
}

func TestRetryExhaustionFails(t *testing.T) {
	llm := &flakyLLM{failN: 10, err: errors.New("connection reset by peer")}
	a := newRetryAgent(t, llm, nil)

	if _, err := a.DoReasoning(context.Background(), userMsgs("hi"), nil, nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if llm.calls != 4 { // initial + 3 retries
		t.Errorf("llm calls = %d, want 4", llm.calls)
	}
}

func TestRetryCancellationReturnsPromptly(t *testing.T) {
	llm := &flakyLLM{failN: 10, err: errors.New("connection refused")}
	a, err := New(AgentConfig{
		Model:          llm,
		SystemPrompt:   "test",
		RetryMax:       3,
		RetryBaseDelay: 10 * time.Second, // long backoff; cancellation must cut it short
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := a.DoReasoning(ctx, userMsgs("hi"), nil, nil); err == nil {
		t.Fatal("expected error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %v, want prompt return", elapsed)
	}
}

func TestRetryDisabled(t *testing.T) {
	llm := &flakyLLM{failN: 10, err: errors.New("timeout")}
	a, err := New(AgentConfig{Model: llm, SystemPrompt: "test", RetryMax: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.DoReasoning(context.Background(), userMsgs("hi"), nil, nil); err == nil {
		t.Fatal("expected error")
	}
	if llm.calls != 1 {
		t.Errorf("llm calls = %d, want 1 (retries disabled)", llm.calls)
	}
}
