package modelregistry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// stallFirst serves a backend whose first request never gets an answer and
// whose later requests get body. The handler is released before Close.
func stallFirst(t *testing.T, body string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			<-release
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

// returnsWithin fails the test when call has not returned in 15s, and
// hands back its error.
func returnsWithin(t *testing.T, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("call still blocked: provider http_timeout not applied")
		return nil
	}
}

// A provider's http_timeout reaches the client the factory builds, for both
// kinds of client.
func TestFactoryAppliesProviderHTTPTimeout(t *testing.T) {
	d := config.Duration(50 * time.Millisecond)

	// Ollama does not retry a timed-out attempt: the call just fails.
	t.Run("llm", func(t *testing.T) {
		srv := stallFirst(t, "")
		llm, err := buildLLM(ResolvedModel{
			Def:      common.ModelDefinition{Name: "m", MaxTokens: 16},
			Provider: config.Provider{Name: "p", Type: "ollama", URL: srv.URL, HTTPTimeout: &d},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = returnsWithin(t, func() error {
			_, err := llm.SendMessageWithTools(context.Background(), common.CompletionRequest{
				Model:    "m",
				Messages: []common.Message{common.NewUserMessage("hi")},
			}, nil)
			return err
		})
		if err == nil {
			t.Fatal("stalled backend returned no error")
		}
	})

	// System One retries a timed-out attempt, so the second one answers.
	t.Run("systemone", func(t *testing.T) {
		srv := stallFirst(t, `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.9}}}`)
		client, err := buildSystemOne(ResolvedSystemOne{
			Name:     "jev",
			Provider: config.Provider{Name: "so", Type: config.SystemOneProviderType, URL: srv.URL, APIKey: "k", HTTPTimeout: &d},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = returnsWithin(t, func() error {
			_, err := client.Evaluate(context.Background(), common.EvaluationRequest{
				State:     "hi",
				Questions: map[string]common.Question{"q": common.NewNoul("Is this a greeting?")},
			})
			return err
		})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
	})
}
