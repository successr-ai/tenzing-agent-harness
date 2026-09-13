package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"runtime/debug"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// maxTokensStdResponse caps output tokens per LLM request.
const maxTokensStdResponse int64 = 32768

// Default transient-error retry policy for LLM calls.
const (
	defaultRetryMax       = 3
	defaultRetryBaseDelay = 2 * time.Second
)

var _ core.Agent = (*Agent)(nil)

type Agent struct {
	model        common.LLM
	systemPrompt string

	streamCallback   func(text string)
	thinkingCallback func(text string)

	// think toggles model reasoning per request; nil leaves it to the
	// provider default.
	think *bool
	// thinkingBudget caps reasoning tokens per request; nil = provider default.
	thinkingBudget *int64

	// Transient-error retry policy for LLM calls. retryMax 0 disables.
	retryMax   int
	retryBase  time.Duration
	onLLMRetry func(attempt, maxRetries int, err error, delay time.Duration)
}

type AgentConfig struct {
	Model        common.LLM
	SystemPrompt string
	// Think toggles model reasoning; nil leaves the provider default.
	Think *bool
	// ThinkingBudget caps reasoning tokens per request; nil leaves the
	// provider default. Clamped below the request MaxTokens at construction.
	ThinkingBudget *int64
	// RetryMax bounds transient-LLM-error retries: 0 means the default (3),
	// negative disables retries. RetryBaseDelay 0 means the default (2s).
	RetryMax       int
	RetryBaseDelay time.Duration
	// OnLLMRetry, when set, is called before each retry sleep.
	OnLLMRetry func(attempt, maxRetries int, err error, delay time.Duration)
}

type agentOptions struct {
	systemPrompt string
}

type ConfigOption func(*agentOptions)

// WithSystemPrompt configures the Agent with a default system prompt
func WithSystemPrompt(prompt string) ConfigOption {
	return func(o *agentOptions) {
		if prompt != "" {
			o.systemPrompt = prompt
		}
	}
}

func New(cfg AgentConfig, opts ...ConfigOption) (*Agent, error) {
	o := &agentOptions{
		systemPrompt: "", // TODO: insert default system prompt?
	}
	for _, opt := range opts {
		opt(o)
	}

	retryMax := cfg.RetryMax
	switch {
	case retryMax == 0:
		retryMax = defaultRetryMax
	case retryMax < 0:
		retryMax = 0
	}
	retryBase := cfg.RetryBaseDelay
	if retryBase <= 0 {
		retryBase = defaultRetryBaseDelay
	}

	budget := cfg.ThinkingBudget
	if budget != nil && *budget >= maxTokensStdResponse {
		// Anthropic requires budget_tokens < max_tokens; keep it satisfiable.
		clamped := maxTokensStdResponse - 1024
		slog.Warn("thinking budget clamped below the per-request max tokens",
			"budget", *budget, "max_tokens", maxTokensStdResponse, "clamped", clamped)
		budget = &clamped
	}

	return &Agent{
		model:          cfg.Model,
		systemPrompt:   cfg.SystemPrompt,
		think:          cfg.Think,
		thinkingBudget: budget,
		retryMax:       retryMax,
		retryBase:      retryBase,
		onLLMRetry:     cfg.OnLLMRetry,
	}, nil
}

func (a *Agent) GetCurrentModel() string {
	return a.model.GetCurrentModel()
}

// SetLLM swaps the underlying LLM client (mid-session model switching).
// Only call between turns — DoReasoning must not be in flight.
func (a *Agent) SetLLM(llm common.LLM) {
	a.model = llm
}

// SetThinking toggles model reasoning for subsequent requests.
func (a *Agent) SetThinking(enabled bool) {
	a.think = &enabled
}

// isTransientLLMError classifies errors worth retrying: network failures,
// timeouts, and 5xx/overload responses — not 4xx-style request errors.
// Provider errors carry no structured status, so this is pattern-based.
func isTransientLLMError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused", "connection reset", "broken pipe", "unexpected eof",
		"timeout", "timed out", "no such host",
		"500", "502", "503", "504", "529",
		"internal server error", "bad gateway", "service unavailable",
		"gateway timeout", "overloaded", "rate limit", "too many requests", "429",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// callLLM performs the request with transient-error retries (exponential
// backoff + jitter, ctx-aware). A streaming call that already emitted
// deltas is never retried — the user would see duplicated text.
func (a *Agent) callLLM(ctx context.Context, req common.CompletionRequest, tools []common.ToolDefinition) (common.CompletionResponse, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		var resp common.CompletionResponse
		var err error
		streamed := false
		if a.streamCallback != nil {
			resp, err = a.doStreamingReasoning(ctx, req, &streamed)
		} else {
			resp, err = a.model.SendMessageWithTools(ctx, req, tools)
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil || streamed || attempt >= a.retryMax || !isTransientLLMError(err) {
			return common.CompletionResponse{}, lastErr
		}

		delay := a.retryBase << attempt
		delay += time.Duration(rand.Int63n(int64(delay)/2 + 1)) // up to +50% jitter
		if a.onLLMRetry != nil {
			a.onLLMRetry(attempt+1, a.retryMax, err, delay)
		}
		slog.Warn("transient llm error, retrying", "attempt", attempt+1, "max", a.retryMax, "delay", delay.Round(time.Millisecond), "error", err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return common.CompletionResponse{}, lastErr
		}
	}
}

func (a *Agent) UpdateStreamCallback(fn func(text string)) {
	a.streamCallback = fn
}

func (a *Agent) UpdateThinkingCallback(fn func(text string)) {
	a.thinkingCallback = fn
}

func (a *Agent) doStreamingReasoning(ctx context.Context, req common.CompletionRequest, streamed *bool) (common.CompletionResponse, error) {
	events := make(chan common.StreamEvent)

	var streamErr error
	done := make(chan struct{})
	go func() {
		streamErr = a.model.SendStreamingMessage(ctx, req, events)
		close(done)
	}()

	var resp common.CompletionResponse
	for event := range events {
		switch event.Type {
		case common.StreamEventDelta:
			*streamed = true
			a.streamCallback(event.Text)
		case common.StreamEventThinking:
			if a.thinkingCallback != nil {
				a.thinkingCallback(event.Text)
			}
		case common.StreamEventStop:
			if event.Response != nil {
				resp = *event.Response
			}
		case common.StreamEventError:
			return common.CompletionResponse{}, event.Err
		}
	}

	<-done
	if streamErr != nil {
		return common.CompletionResponse{}, streamErr
	}
	return resp, nil
}

// DoReasoning is a single, stateless model call: messages is the full
// conversation history, built and owned by the caller's ContextPort, and
// tools is the tool surface for this turn, owned by the caller's ToolPort.
// The agent neither stores nor mutates them — the returned
// Meta.AssistantMessage carries the model's response back for the caller to
// append.
func (a *Agent) DoReasoning(ctx context.Context, messages []common.Message, systemReminders []string, tools []common.ToolDefinition) (core.ReasoningResult, error) {
	// add system reminders to system prompt
	system := a.systemPrompt
	for _, r := range systemReminders {
		system += "\n\n" + r
	}

	// create LLM request
	model := a.model.GetCurrentModel()
	req := common.CompletionRequest{
		Model:          model,
		System:         system,
		Messages:       messages,
		MaxTokens:      maxTokensStdResponse,
		Tools:          tools,
		Think:          a.think,
		ThinkingBudget: a.thinkingBudget,
		// Prompt caching: Anthropic honors it (cache_control breakpoints on
		// system prompt + tools), other providers ignore it.
		CacheSystemAndTools: true,
	}

	slog.Debug("llm request", "model", model, "messages", len(messages), "tools", len(tools))
	if slog.Default().Enabled(ctx, core.LevelTrace) {
		slog.Log(ctx, core.LevelTrace, "llm request system prompt", "model", model, "system", system)
		if raw, err := json.Marshal(messages); err == nil {
			slog.Log(ctx, core.LevelTrace, "llm request messages", "model", model, "messages_json", string(raw))
		}
		if raw, err := json.Marshal(tools); err == nil {
			slog.Log(ctx, core.LevelTrace, "llm request tools", "model", model, "tools_json", string(raw))
		}
	}

	// get the LLM response (with transient-error retries)
	resp, err := a.callLLM(ctx, req, tools)
	if err != nil {
		slog.Error("llm call failed", "model", model, "error", err, "messages", len(messages), "stack", string(debug.Stack()))
		return core.ReasoningResult{}, fmt.Errorf("llm call (%s): %w", model, err)
	}

	slog.Debug("llm response", "model", resp.Model, "response_id", resp.ID, "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens, "cache_read_tokens", resp.Usage.CacheReadInputTokens, "cache_creation_tokens", resp.Usage.CacheCreationInputTokens, "stop_reason", resp.StopReason)
	if text := resp.Text(); text != "" {
		slog.Debug("assistant text", "text", text)
	}

	// get the response details for logging
	meta := core.ResponseMeta{
		Model:                    resp.Model,
		ResponseID:               resp.ID,
		InputTokens:              resp.Usage.InputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
		StopReason:               string(resp.StopReason),
		AssistantText:            resp.Text(),
		AssistantMessage:         common.Message{Role: common.RoleAssistant, Content: resp.Content},
	}

	// if the action to take is tool calls, return all of them; the runner
	// executes each and feeds the results back in the same order.
	toolCalls := resp.ToolCalls()
	if len(toolCalls) > 0 {
		calls := make([]core.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			calls[i] = core.ToolCall{
				ID:    tc.ToolUseID,
				Name:  tc.ToolName,
				Input: string(tc.ToolInput),
			}
		}
		return core.ReasoningResult{
			ToolCalls: calls,
			Meta:      meta,
		}, nil
	}

	// if there are no tool calls, then just return the response
	return core.ReasoningResult{
		FinalAnswer: resp.Text(),
		Meta:        meta,
	}, nil
}
