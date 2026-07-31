package openai_compat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// newErrorTestCompat builds a non-retrying compat client against a server
// that always fails with the given status.
func newErrorTestCompat(t *testing.T, status int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error": {"message": "boom", "type": "server_error"}}`))
	}))
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	return &Client{
		Name:   "openai",
		Client: &client,
		Model:  common.ModelDefinition{Name: "test-model", MaxTokens: 1024},
	}
}

func TestOpenAICompat_SyncErrorsAreAPIErrors(t *testing.T) {
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
			compat := newErrorTestCompat(t, tt.status)
			_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:    "test-model",
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
			if apiErr.Provider != "openai" {
				t.Errorf("Provider = %q, want openai", apiErr.Provider)
			}
			if apiErr.Transient() != tt.wantTransient {
				t.Errorf("Transient() = %v, want %v", apiErr.Transient(), tt.wantTransient)
			}
		})
	}
}

func TestOpenAICompat_StreamingErrorEventCarriesAPIError(t *testing.T) {
	compat := newErrorTestCompat(t, http.StatusServiceUnavailable)

	events := make(chan common.StreamEvent, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- compat.SendStreamingMessage(context.Background(), common.CompletionRequest{
			Model:    "test-model",
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
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", apiErr.StatusCode)
	}
}

func TestOpenAICompat_RetryExhaustedReturnsAPIError(t *testing.T) {
	compat, _ := newRetryTestCompat(t, int32(testMaxRetries)+1, respondCompletion(t))

	_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	var apiErr *common.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *common.APIError", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", apiErr.StatusCode)
	}
	if !apiErr.Transient() {
		t.Error("429 should be transient")
	}
}
