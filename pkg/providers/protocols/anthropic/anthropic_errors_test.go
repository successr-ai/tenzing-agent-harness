package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// newErrorTestClient builds a client against a server that always fails with
// the given status. SDK-internal retries are disabled so 5xx tests don't
// spend wall-clock time backing off.
func newErrorTestClient(t *testing.T, status int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)
	sdkClient := anthropicSDK.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	return &Client{client: &sdkClient, model: testModel}
}

func TestAnthropic_SyncErrorsAreAPIErrors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantTransient bool
	}{
		{"503 transient", http.StatusServiceUnavailable, true},
		{"400 permanent", http.StatusBadRequest, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newErrorTestClient(t, tt.status)
			_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:    "claude-sonnet-4-6",
				Messages: []common.Message{common.NewUserMessage("hi")},
			})
			if err == nil {
				t.Fatal("want error, got nil")
			}
			var apiErr *common.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a *common.APIError", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if apiErr.Provider != "anthropic" {
				t.Errorf("Provider = %q, want anthropic", apiErr.Provider)
			}
			if apiErr.Transient() != tt.wantTransient {
				t.Errorf("Transient() = %v, want %v", apiErr.Transient(), tt.wantTransient)
			}
		})
	}
}

func TestAnthropic_StreamingErrorEventCarriesAPIError(t *testing.T) {
	client := newErrorTestClient(t, http.StatusServiceUnavailable)

	events := make(chan common.StreamEvent, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.SendStreamingMessage(context.Background(), common.CompletionRequest{
			Model:    "claude-sonnet-4-6",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}, events)
	}()

	var eventErr error
	for ev := range events {
		if ev.Type == common.StreamEventError {
			eventErr = ev.Err
		}
	}
	if err := <-errCh; err == nil {
		t.Fatal("want streaming error, got nil")
	}
	if eventErr == nil {
		t.Fatal("no error event delivered")
	}
	var apiErr *common.APIError
	if !errors.As(eventErr, &apiErr) {
		t.Fatalf("event error %v is not a *common.APIError", eventErr)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Provider != "anthropic" {
		t.Errorf("APIError = %+v, want status 503 provider anthropic", apiErr)
	}
}
