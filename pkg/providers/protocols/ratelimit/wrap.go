// Package ratelimit is the single home for client-side rate limiting. It
// provides the Limiter primitives (TokenBucket, Semaphore) and Wrap, a
// common.LLM decorator that guards the send methods of any provider client.
package ratelimit

import (
	"context"
	"errors"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Limiter guards calls. Acquire blocks until the request may proceed;
// Release must be called once per successful Acquire when the work is done.
// Implementations: TokenBucket, Semaphore.
type Limiter interface {
	Acquire(ctx context.Context, cost int64) error
	Release()
}

// CostFunc computes the Acquire cost for a request.
type CostFunc func(ctx context.Context, llm common.LLM, req common.CompletionRequest) (int64, error)

// CostPerRequest charges one unit per request.
func CostPerRequest(_ context.Context, _ common.LLM, _ common.CompletionRequest) (int64, error) {
	return 1, nil
}

// CostByTokenCount charges the request's estimated input tokens, using the
// client's own CountTokens. Providers that don't support token counting
// (common.ErrNotSupported) fall back to one unit per request.
func CostByTokenCount(ctx context.Context, llm common.LLM, req common.CompletionRequest) (int64, error) {
	tokenCount, err := llm.CountTokens(ctx, req)
	if errors.Is(err, common.ErrNotSupported) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("rate limit cost: count tokens: %w", err)
	}
	return tokenCount.InputTokens, nil
}

// Wrap returns llm with its send methods guarded by l. A nil Limiter returns
// llm unchanged. A nil CostFunc defaults to CostPerRequest. CountTokens,
// ListModels, GetCurrentModel, and GetContextWindowSize pass through
// unlimited.
func Wrap(llm common.LLM, l Limiter, cost CostFunc) common.LLM {
	if l == nil {
		return llm
	}
	if cost == nil {
		cost = CostPerRequest
	}
	return &limitedLLM{inner: llm, limiter: l, cost: cost}
}

type limitedLLM struct {
	inner   common.LLM
	limiter Limiter
	cost    CostFunc
}

var _ common.LLM = (*limitedLLM)(nil)

// acquire computes the request cost and takes the limiter. On success the
// caller must call w.limiter.Release when the work is done.
func (w *limitedLLM) acquire(ctx context.Context, req common.CompletionRequest) error {
	cost, err := w.cost(ctx, w.inner, req)
	if err != nil {
		return err
	}
	if err := w.limiter.Acquire(ctx, cost); err != nil {
		return fmt.Errorf("rate limiter acquire: %w", err)
	}
	return nil
}

func (w *limitedLLM) SendSyncMessage(ctx context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	if err := w.acquire(ctx, req); err != nil {
		return common.CompletionResponse{}, err
	}
	defer w.limiter.Release()
	return w.inner.SendSyncMessage(ctx, req)
}

// SendStreamingMessage guards the whole stream: the limiter is held until the
// inner call returns (stream fully drained). If Acquire fails the inner
// client never runs, so the wrapper closes the events channel itself to honor
// the streaming contract.
func (w *limitedLLM) SendStreamingMessage(ctx context.Context, req common.CompletionRequest, events chan<- common.StreamEvent) error {
	if err := w.acquire(ctx, req); err != nil {
		close(events)
		return err
	}
	defer w.limiter.Release()
	return w.inner.SendStreamingMessage(ctx, req, events)
}

func (w *limitedLLM) SendMessageWithTools(ctx context.Context, req common.CompletionRequest, tools []common.ToolDefinition) (common.CompletionResponse, error) {
	if err := w.acquire(ctx, req); err != nil {
		return common.CompletionResponse{}, err
	}
	defer w.limiter.Release()
	return w.inner.SendMessageWithTools(ctx, req, tools)
}

func (w *limitedLLM) CountTokens(ctx context.Context, req common.CompletionRequest) (common.TokenCount, error) {
	return w.inner.CountTokens(ctx, req)
}

func (w *limitedLLM) ListModels(ctx context.Context) ([]common.ModelInfo, error) {
	return w.inner.ListModels(ctx)
}

func (w *limitedLLM) GetCurrentModel() string {
	return w.inner.GetCurrentModel()
}

func (w *limitedLLM) GetContextWindowSize() int {
	return w.inner.GetContextWindowSize()
}

func (w *limitedLLM) GetModel() common.Model {
	return w.inner.GetModel()
}
