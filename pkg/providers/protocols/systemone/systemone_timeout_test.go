package systemone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"
)

// An endpoint that accepts the request and never answers must not block
// Evaluate forever once WithHTTPTimeout is set, whichever order it and
// WithHTTPClient are given in.
func TestHTTPTimeoutUnblocksStalledEndpoint(t *testing.T) {
	// The handler never answers; the test releases it before Close
	// (cleanups run last-in, first-out).
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	timeout := WithHTTPTimeout(50 * time.Millisecond)
	custom := WithHTTPClient(&http.Client{})
	tests := []struct {
		name string
		opts []ClientOption
	}{
		{"default client", []ClientOption{timeout}},
		{"timeout before custom client", []ClientOption{timeout, custom}},
		{"timeout after custom client", []ClientOption{custom, timeout}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := append([]ClientOption{
				WithAPIKey("k"), WithURL(srv.URL), WithModel("jev-latest"),
				WithRetryBackoff(ratelimit.RetryBackoff{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}),
			}, tt.opts...)
			client, err := NewClient(opts...)
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() {
				_, err := client.Evaluate(context.Background(), common.EvaluationRequest{
					State:     "hi",
					Questions: map[string]common.Question{"q": common.NewNoul("Is this a greeting?")},
				})
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled endpoint returned no error")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("Evaluate still blocked on a stalled endpoint")
			}
		})
	}
}
