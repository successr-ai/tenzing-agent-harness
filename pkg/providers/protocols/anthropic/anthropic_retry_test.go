package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const anthropicMessageJSON = `{
	"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
	"content": [{"type": "text", "text": "hello"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 10, "output_tokens": 5}
}`

// newRetryTestClient returns a retrying client whose first reject429 requests
// are rejected with 429, plus a counter of requests received. SDK-internal
// retries are disabled so the provider's retry loop is what's under test.
func newRetryTestClient(t *testing.T, reject429 int32) (*Client, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) <= reject429 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
			return
		}
		if _, err := w.Write([]byte(anthropicMessageJSON)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	sdkClient := anthropicSDK.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	backoff := ratelimit.RetryBackoff{
		MaxRetries:  3,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	}
	return &Client{client: &sdkClient, model: testModel, retryBackoff: &backoff}, &requests
}

func TestAnthropic_SyncRetriesOnRateLimit(t *testing.T) {
	client, requests := newRetryTestClient(t, 2)

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
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

func TestAnthropic_RetryEnabledByDefault(t *testing.T) {
	llm, err := NewClient(testModel)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client := llm.(*Client)
	if client.retryBackoff == nil || *client.retryBackoff != ratelimit.NewDefaultBackoff() {
		t.Errorf("retryBackoff = %+v, want defaults (429 retry on by default)", client.retryBackoff)
	}
}

func TestAnthropic_NoRetryWhenDisabled(t *testing.T) {
	client, requests := newRetryTestClient(t, 1)
	client.retryBackoff = nil

	_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1 (no retry)", got)
	}
}
