package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"api error 429", &common.APIError{StatusCode: 429}, true},
		{"wrapped api error 429", fmt.Errorf("send: %w", &common.APIError{StatusCode: 429}), true},
		{"api error 500", &common.APIError{StatusCode: 500}, false},
		{"string fallback", errors.New("unexpected status 429 Too Many Requests"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRateLimited(tt.err); got != tt.expected {
				t.Errorf("IsRateLimited() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// fastBackoff keeps retry tests quick.
func fastBackoff(maxRetries int) *RetryBackoff {
	return &RetryBackoff{
		MaxRetries:  maxRetries,
		BaseBackoff: time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	rateLimited := &common.APIError{StatusCode: 429}

	t.Run("retries until success", func(t *testing.T) {
		calls := 0
		got, err := RetryOnRateLimit(context.Background(), "test", fastBackoff(5), func() (string, error) {
			calls++
			if calls < 3 {
				return "", rateLimited
			}
			return "ok", nil
		})
		if err != nil || got != "ok" {
			t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("exhausts attempts", func(t *testing.T) {
		calls := 0
		_, err := RetryOnRateLimit(context.Background(), "test", fastBackoff(3), func() (string, error) {
			calls++
			return "", rateLimited
		})
		if err == nil {
			t.Fatal("want error after exhausting retries, got nil")
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("nil cfg means single attempt", func(t *testing.T) {
		calls := 0
		_, err := RetryOnRateLimit(context.Background(), "test", nil, func() (string, error) {
			calls++
			return "", rateLimited
		})
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("non-429 errors are not retried", func(t *testing.T) {
		calls := 0
		boom := &common.APIError{StatusCode: 500}
		_, err := RetryOnRateLimit(context.Background(), "test", fastBackoff(5), func() (string, error) {
			calls++
			return "", boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
}

func TestRetryStreaming(t *testing.T) {
	rateLimited := &common.APIError{StatusCode: 429}

	t.Run("retries while nothing emitted", func(t *testing.T) {
		calls := 0
		err := RetryStreaming(context.Background(), "test", fastBackoff(5), func() (bool, error) {
			calls++
			if calls < 3 {
				return false, rateLimited
			}
			return true, nil
		})
		if err != nil {
			t.Fatalf("RetryStreaming: %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("no retry after events emitted", func(t *testing.T) {
		calls := 0
		err := RetryStreaming(context.Background(), "test", fastBackoff(5), func() (bool, error) {
			calls++
			return true, rateLimited
		})
		if !errors.Is(err, rateLimited) {
			t.Fatalf("err = %v, want %v", err, rateLimited)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})
}
