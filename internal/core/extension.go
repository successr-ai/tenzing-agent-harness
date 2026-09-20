package core

import (
	"context"
	"log/slog"
	"time"
)

// Extension is a named bundle of optional hooks, tools, and prompt fragments.
// Implement only the capability interfaces you need.
type Extension interface {
	Name() string
}

// Decision is the loop's tool-gating vocabulary. Zero value is Allow.
// The loop's response to each value is invariant; policy lives in extensions.
type Decision int

const (
	Allow Decision = iota
	AskUser
	Deny
)

// TurnContext is passed to BeforeIteration hooks each iteration. The loop
// populates the usage fields (elapsed time and cumulative token counts for
// this turn) before running the hooks, so budget-style hooks can decide on
// them.
type TurnContext struct {
	RunnerID  string
	Iteration int
	Reminders []string // hooks append; delivered as system reminders
	Terminate string   // non-empty: graceful termination with this reason

	Elapsed      time.Duration // time since the turn started
	InputTokens  int64         // cumulative this turn
	OutputTokens int64
}

// ToolCallContext is passed to OnToolCall hooks per call, in issue order.
// Hooks may mutate Call.Input and escalate Decision (never de-escalate:
// the loop takes the most restrictive decision observed).
type ToolCallContext struct {
	RunnerID string
	Call     *ToolCall
	Origin   string // "native", "mcp:<server>", "extension:<name>"
	Decision Decision
	Reason   string
}

// ToolResultContext is passed to OnToolResult hooks. Hooks may transform
// Result; a hook error rolls the transform back (post-hooks degrade).
type ToolResultContext struct {
	RunnerID string
	Call     ToolCall
	Result   *ToolResult
}

// TurnResult is the outcome of one RunTurn.
type TurnResult struct {
	FinalAnswer string
	Iterations  int
	Duration    time.Duration
	Terminated  string // non-empty: graceful termination reason (budget, cancel, …)
}

// Optional capability interfaces. Pre-hooks (BeforeIteration, OnToolCall)
// are load-bearing: an error blocks the operation. Post-hooks (OnToolResult,
// AfterTurn) degrade: errors are logged, never fatal.
type SessionStartHook interface {
	SessionStart(ctx context.Context) error
}
type SessionEndHook interface {
	SessionEnd(ctx context.Context) error
}
type BeforeIterationHook interface {
	BeforeIteration(ctx context.Context, tc *TurnContext) error
}
type ToolCallHook interface {
	OnToolCall(ctx context.Context, tcc *ToolCallContext) error
}

// ToolBatchHook sees every tool call of one iteration at once, before the
// per-call OnToolCall hooks run. It exists for policies whose decision is
// cheaper or better made over the whole batch than call by call — one
// request to a judging model instead of one per call. Same contract as
// OnToolCall otherwise: load-bearing, may mutate each Call.Input, may
// escalate each Decision and never lower one. The loop passes the same
// contexts on to the per-call hooks, so an escalation here survives.
type ToolBatchHook interface {
	OnToolBatch(ctx context.Context, batch []*ToolCallContext) error
}
type ToolResultHook interface {
	OnToolResult(ctx context.Context, trc *ToolResultContext) error
}
type AfterTurnHook interface {
	AfterTurn(ctx context.Context, tr *TurnResult) error
}
type PromptContributor interface {
	PromptFragment() string
}

// ToolProvider is a static tool bundle contributed by an extension. Specs are
// read once at composite construction.
type ToolProvider interface {
	Tools() []ToolSpec
}

// DynamicToolSource is a tool source re-read at each turn boundary (MCP).
type DynamicToolSource interface {
	CurrentTools(ctx context.Context) []ToolSpec
}

// Extensions holds registered extensions bucketed by capability. Buckets are
// filled once at construction; the loop iterates plain slices.
type Extensions struct {
	all             []Extension
	sessionStart    []SessionStartHook
	sessionEnd      []SessionEndHook
	beforeIteration []BeforeIterationHook
	toolBatch       []ToolBatchHook
	toolCall        []ToolCallHook
	toolResult      []ToolResultHook
	afterTurn       []AfterTurnHook
	prompts         []PromptContributor
	toolProviders   []ToolProvider
	dynamicSources  []DynamicToolSource
}

func NewExtensions(exts ...Extension) *Extensions {
	e := &Extensions{all: exts}
	for _, ext := range exts {
		var hooks []string
		if h, ok := ext.(SessionStartHook); ok {
			e.sessionStart = append(e.sessionStart, h)
			hooks = append(hooks, "session_start")
		}
		if h, ok := ext.(SessionEndHook); ok {
			e.sessionEnd = append(e.sessionEnd, h)
			hooks = append(hooks, "session_end")
		}
		if h, ok := ext.(BeforeIterationHook); ok {
			e.beforeIteration = append(e.beforeIteration, h)
			hooks = append(hooks, "before_iteration")
		}
		if h, ok := ext.(ToolBatchHook); ok {
			e.toolBatch = append(e.toolBatch, h)
			hooks = append(hooks, "tool_batch")
		}
		if h, ok := ext.(ToolCallHook); ok {
			e.toolCall = append(e.toolCall, h)
			hooks = append(hooks, "tool_call")
		}
		if h, ok := ext.(ToolResultHook); ok {
			e.toolResult = append(e.toolResult, h)
			hooks = append(hooks, "tool_result")
		}
		if h, ok := ext.(AfterTurnHook); ok {
			e.afterTurn = append(e.afterTurn, h)
			hooks = append(hooks, "after_turn")
		}
		if h, ok := ext.(PromptContributor); ok {
			e.prompts = append(e.prompts, h)
			hooks = append(hooks, "prompt")
		}
		if h, ok := ext.(ToolProvider); ok {
			e.toolProviders = append(e.toolProviders, h)
			hooks = append(hooks, "tools")
		}
		if h, ok := ext.(DynamicToolSource); ok {
			e.dynamicSources = append(e.dynamicSources, h)
			hooks = append(hooks, "dynamic_tools")
		}
		slog.Info("extension registered", "ext", ext.Name(), "hooks", hooks)
	}
	return e
}

// RunSessionStart / RunSessionEnd: load-bearing on start, degrading on end.
func (e *Extensions) RunSessionStart(ctx context.Context) error {
	for _, h := range e.sessionStart {
		if err := h.SessionStart(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (e *Extensions) RunSessionEnd(ctx context.Context) {
	for _, h := range e.sessionEnd {
		if err := h.SessionEnd(ctx); err != nil {
			slog.Warn("session end hook failed", "error", err)
		}
	}
}

// RunBeforeIteration is load-bearing: the first error blocks the iteration.
func (e *Extensions) RunBeforeIteration(ctx context.Context, tc *TurnContext) error {
	for _, h := range e.beforeIteration {
		if err := h.BeforeIteration(ctx, tc); err != nil {
			return err
		}
	}
	return nil
}

// RunToolBatch is load-bearing, and runs before any RunToolCall of the same
// iteration. De-escalation is restored per call, as in RunToolCall.
func (e *Extensions) RunToolBatch(ctx context.Context, batch []*ToolCallContext) error {
	for _, h := range e.toolBatch {
		before := make([]Decision, len(batch))
		for i, tcc := range batch {
			before[i] = tcc.Decision
		}
		if err := h.OnToolBatch(ctx, batch); err != nil {
			return err
		}
		for i, tcc := range batch {
			if tcc.Decision < before[i] { // de-escalation attempt: restore
				tcc.Decision = before[i]
			}
		}
	}
	return nil
}

// RunToolCall is load-bearing. All hooks run even after a Deny (later hooks
// may escalate Deny→nothing-more-restrictive; the most restrictive decision
// observed wins — hooks must not lower an existing Decision).
func (e *Extensions) RunToolCall(ctx context.Context, tcc *ToolCallContext) error {
	for _, h := range e.toolCall {
		before := tcc.Decision
		if err := h.OnToolCall(ctx, tcc); err != nil {
			return err
		}
		if tcc.Decision < before { // de-escalation attempt: restore
			tcc.Decision = before
		}
	}
	return nil
}

// RunToolResult degrades: a hook error rolls back that hook's transform and
// continues with the remaining hooks.
func (e *Extensions) RunToolResult(ctx context.Context, trc *ToolResultContext) {
	for _, h := range e.toolResult {
		saved := *trc.Result
		if trc.Result.Metadata != nil {
			saved.Metadata = make(map[string]string, len(trc.Result.Metadata))
			for k, v := range trc.Result.Metadata {
				saved.Metadata[k] = v
			}
		}
		if err := h.OnToolResult(ctx, trc); err != nil {
			*trc.Result = saved
			slog.Warn("tool result hook failed; transform rolled back", "error", err)
		}
	}
}

// RunAfterTurn degrades: errors log only.
func (e *Extensions) RunAfterTurn(ctx context.Context, tr *TurnResult) {
	for _, h := range e.afterTurn {
		if err := h.AfterTurn(ctx, tr); err != nil {
			slog.Warn("after turn hook failed", "error", err)
		}
	}
}

// ToolProviders returns static tool bundles in registration order.
func (e *Extensions) ToolProviders() []ToolProvider { return e.toolProviders }

// DynamicToolSources returns dynamic tool sources in registration order.
func (e *Extensions) DynamicToolSources() []DynamicToolSource { return e.dynamicSources }

// PromptFragments returns contributions in registration order.
func (e *Extensions) PromptFragments() []string {
	frags := make([]string, 0, len(e.prompts))
	for _, p := range e.prompts {
		if f := p.PromptFragment(); f != "" {
			frags = append(frags, f)
		}
	}
	return frags
}
