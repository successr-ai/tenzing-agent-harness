package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"
)

// newRetryTestOllama returns a retrying client whose first reject429 requests
// are rejected with 429, plus a counter of requests received.
func newRetryTestOllama(t *testing.T, reject429 int32) (common.LLM, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) <= reject429 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		if _, err := w.Write([]byte(`{
			"model": "test-model",
			"message": {"role": "assistant", "content": "hello"},
			"done": true, "done_reason": "stop",
			"prompt_eval_count": 8, "eval_count": 4
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return mustNewClient(t, WithBaseURL(srv.URL), WithRetryBackoff(ratelimit.RetryBackoff{
		MaxRetries:  3,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	})), &requests
}

func TestOllama_SyncRetriesOnRateLimit(t *testing.T) {
	client, requests := newRetryTestOllama(t, 2)

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	if got := requests.Load(); got != 3 {
		t.Errorf("server received %d requests, want 3 (two 429s then success)", got)
	}
	if res.Text() != "hello" {
		t.Errorf("text = %q, want hello", res.Text())
	}
}

func TestOllama_RetryEnabledByDefault(t *testing.T) {
	client := mustNewClient(t).(*Client)
	if client.retryBackoff == nil || *client.retryBackoff != ratelimit.NewDefaultBackoff() {
		t.Errorf("retryBackoff = %+v, want defaults (429 retry on by default)", client.retryBackoff)
	}
}

func TestOllama_NoRetryWhenDisabled(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(srv.Close)
	client := mustNewClient(t, WithBaseURL(srv.URL)).(*Client)
	client.retryBackoff = nil

	_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1 (no retry)", got)
	}
}

func TestOllama_StreamingRetriesBeforeFirstEvent(t *testing.T) {
	client, requests := newRetryTestOllama(t, 1)

	events := make(chan common.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.SendStreamingMessage(context.Background(), common.CompletionRequest{
			Model:    "test-model",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}, events)
	}()

	starts := 0
	for ev := range events {
		switch ev.Type {
		case common.StreamEventStart:
			starts++
		case common.StreamEventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("SendStreamingMessage: %v", err)
	}

	if got := requests.Load(); got != 2 {
		t.Errorf("server received %d requests, want 2 (one 429 then stream)", got)
	}
	if starts != 1 {
		t.Errorf("got %d start events, want exactly 1 (no duplicates from retry)", starts)
	}
}
