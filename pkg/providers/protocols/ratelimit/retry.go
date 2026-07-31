package ratelimit

// Server-side rate-limit (HTTP 429) detection and retry helpers shared by the
// protocol clients. Callers pass errors already wrapped in *common.APIError so
// detection stays provider-agnostic.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// IsRateLimited reports whether err is an HTTP 429, via *common.APIError or
// the status text for errors wrapped outside it.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *common.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429
	}
	msg := err.Error()
	return strings.Contains(msg, "429") && strings.Contains(msg, "Too Many Requests")
}

// Backoff sleeps before retry attempt+1 using exponential backoff with jitter
// per cfg. Returns early with ctx.Err() if the context is cancelled.
func Backoff(ctx context.Context, provider string, attempt int, cfg RetryBackoff) error {
	backoff := float64(cfg.BaseBackoff) * math.Pow(2, float64(attempt))
	backoff = min(backoff, float64(cfg.MaxBackoff))
	jitter := backoff * cfg.BackoffJitter * (rand.Float64()*2 - 1)
	delay := time.Duration(backoff + jitter)

	slog.WarnContext(ctx, "API rate limited, backing off",
		"provider", provider,
		"attempt", attempt+1,
		"delay", delay.Round(time.Millisecond),
	)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RetryOnRateLimit runs fn, retrying rate-limited attempts per cfg. A nil cfg
// disables retries (single attempt). fn must return errors wrapped in
// *common.APIError for 429 detection.
func RetryOnRateLimit[T any](ctx context.Context, provider string, cfg *RetryBackoff, fn func() (T, error)) (T, error) {
	var zero T
	maxAttempts := 1
	if cfg != nil {
		maxAttempts = cfg.OrDefaults().MaxRetries
	}
	for attempt := range maxAttempts {
		result, err := fn()
		if err == nil || !IsRateLimited(err) {
			return result, err
		}
		if attempt == maxAttempts-1 {
			return zero, fmt.Errorf("%s rate limited after %d attempts: %w", provider, maxAttempts, err)
		}
		if err := Backoff(ctx, provider, attempt, cfg.OrDefaults()); err != nil {
			return zero, err
		}
	}
	return zero, nil
}

// RetryStreaming runs fn, a single streaming attempt that reports whether it
// emitted any events. Rate-limited attempts are retried per cfg only while
// nothing has been emitted, so consumers never see duplicated events. A nil
// cfg disables retries. fn must return errors wrapped in *common.APIError.
func RetryStreaming(ctx context.Context, provider string, cfg *RetryBackoff, fn func() (bool, error)) error {
	maxAttempts := 1
	if cfg != nil {
		maxAttempts = cfg.OrDefaults().MaxRetries
	}
	for attempt := range maxAttempts {
		emitted, err := fn()
		if err == nil {
			return nil
		}
		if emitted || !IsRateLimited(err) {
			return err
		}
		if attempt == maxAttempts-1 {
			return fmt.Errorf("%s rate limited after %d attempts: %w", provider, maxAttempts, err)
		}
		if backoffErr := Backoff(ctx, provider, attempt, cfg.OrDefaults()); backoffErr != nil {
			return backoffErr
		}
	}
	return nil
}
