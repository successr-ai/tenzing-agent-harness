package tenzing_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/tenzing"
)

// stallFirst serves an endpoint whose first request never gets an answer and
// whose later requests get body. Without an HTTP timeout the call hangs on
// the first attempt; with one, the attempt times out and the retry succeeds.
func stallFirst(t *testing.T, body string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			<-release
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	// Cleanups run last-in, first-out: release the stalled handler first.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

// within fails the test when call has not returned in 15s.
func within(t *testing.T, call func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("call still blocked on a stalled endpoint")
	}
}

func TestCallLLMHTTPTimeout(t *testing.T) {
	srv := stallFirst(t, `{"id":"c1","object":"chat.completion","model":"vendor/x","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"42"}}]}`)
	within(t, func() error {
		_, err := tenzing.CallLLM(context.Background(), tenzing.LLMCall{
			APIKey: "k", URL: srv.URL, Model: "vendor/x", MaxTokens: 16,
			HTTPTimeout: 50 * time.Millisecond,
			Prompt:      tenzing.Prompt{Text: "hi"},
		})
		return err
	})
}

func TestCallSystemOneHTTPTimeout(t *testing.T) {
	srv := stallFirst(t, `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.9}}}`)
	within(t, func() error {
		_, err := tenzing.CallSystemOne(context.Background(), tenzing.SystemOneCall{
			APIKey: "k", URL: srv.URL, Model: "jev-latest",
			HTTPTimeout: 50 * time.Millisecond,
			State:       "hi",
			Questions:   map[string]tenzing.Question{"q": tenzing.NewNoul("Is this a greeting?")},
		})
		return err
	})
}
