package ollama

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestOllamaOptions_NumCtx(t *testing.T) {
	tests := []struct {
		name        string
		contextSize int64
		wantNumCtx  int64
	}{
		{"defaults to model's default window, not its max", 0, int64(testModel.GetDefaultContextWindow())},
		{"explicit ContextSize overrides", 8192, 8192},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// NewClient returns the raw client by default (no rate limit),
			// so the assertion is safe.
			c := mustNewClient(t, WithContextSize(tt.contextSize)).(*Client)
			opts := c.ollamaOptions(common.CompletionRequest{})
			if got := opts["num_ctx"]; got != tt.wantNumCtx {
				t.Errorf("num_ctx = %v, want %v", got, tt.wantNumCtx)
			}
		})
	}

	if max := testModel.GetContextWindowSize(); testModel.GetDefaultContextWindow() >= max {
		t.Errorf("default window %d should be below model max %d", testModel.GetDefaultContextWindow(), max)
	}
}

func TestOllamaOptions_BaseURL(t *testing.T) {
	tests := []struct {
		name string
		opts []ClientOption
		want string
	}{
		{"defaults to Ollama Cloud", nil, "https://ollama.com"},
		{"WithBaseURL overrides", []ClientOption{WithBaseURL("https://example.test")}, "https://example.test"},
		{"WithBaseURL local server", []ClientOption{WithBaseURL("http://localhost:11434")}, "http://localhost:11434"},
		{"WithBaseURL empty string keeps default", []ClientOption{WithBaseURL("")}, "https://ollama.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustNewClient(t, tt.opts...).(*Client)
			if c.baseURL != tt.want {
				t.Errorf("baseURL = %q, want %q", c.baseURL, tt.want)
			}
		})
	}
}

func TestOllama_ConcurrencyLimitWrapsClient(t *testing.T) {
	client := mustNewClient(t, WithMaxConcurrency(2))
	if _, ok := client.(*Client); ok {
		t.Error("WithMaxConcurrency should wrap the raw client in a limiter")
	}
}

func TestOllama_RateLimitOptions(t *testing.T) {
	client := mustNewClient(t, WithRateLimit(100, 1000))
	if _, ok := client.(*Client); ok {
		t.Error("WithRateLimit should wrap the raw client in a limiter")
	}

	if _, err := NewClient(testModel, WithRateLimit(100, 0)); err == nil {
		t.Error("WithRateLimit without burst size: want error, got nil")
	}
}

func TestGetContextWindowSize_EffectiveWindow(t *testing.T) {
	if got := mustNewClient(t, WithContextSize(8192)).GetContextWindowSize(); got != 8192 {
		t.Errorf("GetContextWindowSize() = %d, want 8192", got)
	}
	if got := mustNewClient(t).GetContextWindowSize(); got != testModel.GetDefaultContextWindow() {
		t.Errorf("GetContextWindowSize() = %d, want model default %d", got, testModel.GetDefaultContextWindow())
	}
}
