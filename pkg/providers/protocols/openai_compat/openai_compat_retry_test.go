package openai_compat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"
)

const compatCompletionJSON = `{
	"id": "chatcmpl-1", "object": "chat.completion", "model": "test-model",
	"choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
	"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`

// newRetryTestCompat returns a retrying client whose first n requests are
// rejected with 429, plus a counter of requests received. SDK-internal
// retries are disabled so the provider's retry loop is what's under test.
// testMaxRetries mirrors the default MaxRetries so retry-exhaustion tests
// stay fast with millisecond backoffs.
const testMaxRetries = 5

func newRetryTestCompat(t *testing.T, reject429 int32, respond http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= reject429 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error": {"message": "rate limited", "type": "rate_limit_error"}}`)
			return
		}
		respond(w, r)
	}))
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	return &Client{
		Name:   "test",
		Client: &client,
		Model:  common.ModelDefinition{Name: "test-model", MaxTokens: 1024},
		RetryBackoff: &ratelimit.RetryBackoff{
			MaxRetries:  testMaxRetries,
			BaseBackoff: time.Millisecond,
			MaxBackoff:  5 * time.Millisecond,
		},
	}, &requests
}

func respondCompletion(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(compatCompletionJSON)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
}

func TestOpenAICompat_SyncRetriesOnRateLimit(t *testing.T) {
	compat, requests := newRetryTestCompat(t, 2, respondCompletion(t))

	res, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
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

func TestOpenAICompat_SyncRetryExhausted(t *testing.T) {
	compat, requests := newRetryTestCompat(t, int32(testMaxRetries)+1, respondCompletion(t))

	_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	if got := requests.Load(); got != int32(testMaxRetries) {
		t.Errorf("server received %d requests, want %d", got, testMaxRetries)
	}
}

func TestOpenAICompat_NoRetryWhenDisabled(t *testing.T) {
	compat, requests := newRetryTestCompat(t, 1, respondCompletion(t))
	compat.RetryBackoff = nil

	_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
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

func TestOpenAICompat_StreamingRetriesBeforeFirstEvent(t *testing.T) {
	compat, requests := newRetryTestCompat(t, 1, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"chatcmpl-1","model":"test-model","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, chunk := range chunks {
			if _, err := w.Write([]byte("data: " + chunk + "\n\n")); err != nil {
				t.Errorf("write chunk: %v", err)
				return
			}
		}
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Errorf("write done: %v", err)
		}
	})

	events := make(chan common.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- compat.SendStreamingMessage(context.Background(), common.CompletionRequest{
			Model:    "test-model",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}, events)
	}()

	starts := 0
	var deltas []string
	for ev := range events {
		switch ev.Type {
		case common.StreamEventStart:
			starts++
		case common.StreamEventDelta:
			deltas = append(deltas, ev.Text)
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
	if len(deltas) != 1 || deltas[0] != "hello" {
		t.Errorf("deltas = %v, want [hello]", deltas)
	}
}
