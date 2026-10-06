package openai_compat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// A provider that accepts the request and never answers must not block the
// call forever once WithHTTPTimeout is set.
func TestHTTPTimeoutUnblocksStalledProvider(t *testing.T) {
	// The handler never answers. It can't rely on r.Context(): the server
	// only notices a client hang-up once the body is read, so the test
	// releases it before Close (cleanups run last-in, first-out).
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	llm, err := NewClient(common.ModelDefinition{Name: "test-model", MaxTokens: 16},
		WithAPIKey("test"), WithBaseURL(srv.URL), WithHTTPTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := llm.SendMessageWithTools(context.Background(), common.CompletionRequest{
			Model:    "test-model",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled provider returned no error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("call still blocked on a stalled provider")
	}
}
