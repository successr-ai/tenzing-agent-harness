package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const logOutputMaxLen = 2000

// steeringBufferSize bounds how many steering messages can wait for the next
// tool-execution boundary before Steer starts rejecting.
const steeringBufferSize = 16

// maxParallelReadOnlyTools bounds how many read-only tool calls from one
// batch execute concurrently.
const maxParallelReadOnlyTools = 8

// maxInvalidFinalRetries bounds how many times an invalid final answer (empty,
// a tool call written as plain text, or a response truncated at the output
// token limit) is bounced back to the model before the loop gives up: a
// non-empty answer is returned as-is, an empty one fails the turn.
const maxInvalidFinalRetries = 2

// toolCallTextRe matches text that is a malformed tool-call attempt emitted as
// prose — e.g. "<|tool_call>...", "call:graph_cypher{...}" — which smaller
// models produce under context pressure instead of a real tool invocation.
var toolCallTextRe = regexp.MustCompile(`(?s)^\s*(<\|+/?tool_call|call:[A-Za-z_]+\s*[{(])`)

// invalidFinalAnswerReason returns a non-empty reason when the answer should
// not be handed to the caller.
func invalidFinalAnswerReason(answer string) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return "the response was empty"
	}
	if toolCallTextRe.MatchString(trimmed) {
		return "the response looks like a tool call written as plain text"
	}
	return ""
}

func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// LoopConfig configures a Loop instance.
type LoopConfig struct {
	ID           string
	Model        ModelPort
	Tools        ToolPort
	Context      ContextPort
	Emitter      Emitter     // nil-safe
	Extensions   *Extensions // nil → NewExtensions()
	SystemPrompt string      // exposed for logging; the ModelPort owns applying it
	FSM          *LoopFSM    // nil → NewLoopFSM()
	// ApprovalTimeout bounds how long an AskUser decision may wait for an
	// ApprovalRequestedEvent response. 0 = deny immediately (safe for
	// unattended drivers with nobody to answer).
	ApprovalTimeout time.Duration
	// SkipPermissions auto-approves every AskUser decision — no
	// ApprovalRequestedEvent is emitted and ApprovalTimeout is irrelevant.
	// Deny decisions still deny. For sandboxed/unattended drivers only.
	SkipPermissions bool
}

// Loop is the invariant agent reasoning-tool execution loop. It owns the FSM,
// drives the ModelPort/ToolPort/ContextPort ports, and runs extension hooks.
type Loop struct {
	id              string
	model           ModelPort
	tools           ToolPort
	context         ContextPort
	emitter         Emitter
	extensions      *Extensions
	sysPrompt       string
	fsm             *LoopFSM
	approvalTimeout time.Duration
	skipPermissions bool
	steerCh         chan string
}

// NewLoop creates a Loop from the given config.
func NewLoop(cfg LoopConfig) (*Loop, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("loop ID is required")
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("ModelPort is required")
	}
	if cfg.Tools == nil {
		return nil, fmt.Errorf("ToolPort is required")
	}
	if cfg.Context == nil {
		return nil, fmt.Errorf("ContextPort is required")
	}
	if cfg.Extensions == nil {
		cfg.Extensions = NewExtensions()
	}
	if cfg.FSM == nil {
		cfg.FSM = NewLoopFSM()
	}
	return &Loop{
		id:              cfg.ID,
		model:           cfg.Model,
		tools:           cfg.Tools,
		context:         cfg.Context,
		emitter:         cfg.Emitter,
		extensions:      cfg.Extensions,
		sysPrompt:       cfg.SystemPrompt,
		fsm:             cfg.FSM,
		approvalTimeout: cfg.ApprovalTimeout,
		skipPermissions: cfg.SkipPermissions,
		steerCh:         make(chan string, steeringBufferSize),
	}, nil
}

// ID returns the loop's identifier.
func (l *Loop) ID() string { return l.id }

// State reports the FSM's current state (e.g. "started", "stopped",
// "reasoning_started"). Safe for concurrent use.
func (l *Loop) State() string { return l.fsm.Current() }

// Steer queues a user message for injection into the running loop at the
// next tool-execution boundary. Messages queued while no loop is running are
// injected at the first boundary of the next turn. Returns an error when the
// steering buffer is full.
func (l *Loop) Steer(msg string) error {
	select {
	case l.steerCh <- msg:
		return nil
	default:
		return fmt.Errorf("steering buffer full (%d pending)", steeringBufferSize)
	}
}

// drainSteering empties the steering buffer without blocking.
func (l *Loop) drainSteering() []string {
	var msgs []string
	for {
		select {
		case m := <-l.steerCh:
			msgs = append(msgs, m)
		default:
			return msgs
		}
	}
}

// batchCall is one tool call after the sequential decision phase, ready for
// concurrent execution.
type batchCall struct {
	call     ToolCall
	decision Decision
	reason   string
	hookErr  error
	approval <-chan bool // non-nil when AskUser with an approver configured
}

// executeSegmented runs one response's decided calls, returning outputs in
// call order. Runs of consecutive read-only calls (per ToolPort.ReadOnly)
// execute concurrently, bounded by maxParallelReadOnlyTools; mutating calls
// act as barriers and run alone. Calls with a Deny decision or a hook error
// never execute, so their read-only classification is irrelevant to safety.
func (l *Loop) executeSegmented(ctx context.Context, iteration int, pending []batchCall) []ToolResult {
	outputs := make([]ToolResult, len(pending))
	i := 0
	for i < len(pending) {
		j := i
		for j < len(pending) && l.tools.ReadOnly(pending[j].call.Name) {
			j++
		}
		if j == i {
			// mutating call: run alone
			j = i + 1
		}
		if j-i == 1 {
			outputs[i] = l.executeBatched(ctx, iteration, pending[i])
		} else {
			sem := make(chan struct{}, maxParallelReadOnlyTools)
			var wg sync.WaitGroup
			for k := i; k < j; k++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					outputs[idx] = l.executeBatched(ctx, iteration, pending[idx])
				}(k)
			}
			wg.Wait()
		}
		i = j
	}
	return outputs
}

// executeBatched runs one decided call: per-call events, decision handling
// (including waiting out a pending approval), execution, and logging. Safe
// to run concurrently with other calls — the emitter contract requires
// goroutine-safe Emit (EventBus provides it).
func (l *Loop) executeBatched(ctx context.Context, iteration int, bc batchCall) ToolResult {
	l.emit(ToolExecutionStartedEvent{
		BaseEvent: NewBaseEvent(EventToolExecutionStarted, l.id),
		ToolName:  bc.call.Name,
		Input:     bc.call.Input,
	})
	toolStart := time.Now()

	var toolResult ToolResult
	switch {
	case bc.hookErr != nil:
		toolResult = ToolResult{ToolUseID: bc.call.ID, Output: fmt.Sprintf("tool call blocked by extension: %v", bc.hookErr), IsError: true}
	case bc.decision == Deny:
		toolResult = ToolResult{ToolUseID: bc.call.ID, Output: fmt.Sprintf("tool call denied by policy: %s", bc.reason), IsError: true}
		l.emitToolDenied(bc.call, bc.reason)
	case bc.decision == AskUser:
		if approved, denyReason := l.waitApproval(ctx, bc.approval, bc.reason); approved {
			toolResult = l.tools.Execute(ctx, bc.call)
		} else {
			toolResult = ToolResult{ToolUseID: bc.call.ID, Output: denyReason, IsError: true}
			l.emitToolDenied(bc.call, denyReason)
		}
	default:
		toolResult = l.tools.Execute(ctx, bc.call)
	}

	toolDuration := time.Since(toolStart)
	if toolResult.IsError {
		slog.Warn("tool error", "runner", l.id, "iter", iteration, "tool", bc.call.Name, "output", truncateLog(toolResult.Output, logOutputMaxLen))
	}
	slog.Debug("tool result", "runner", l.id, "iter", iteration, "tool", bc.call.Name, "is_error", toolResult.IsError, "duration", toolDuration.Round(time.Millisecond), "output_len", len(toolResult.Output), "output", truncateLog(toolResult.Output, logOutputMaxLen))
	slog.Log(ctx, LevelTrace, "tool result full", "runner", l.id, "iter", iteration, "tool", bc.call.Name, "output", toolResult.Output)

	l.emit(ToolExecutionFinishedEvent{
		BaseEvent: NewBaseEvent(EventToolExecutionFinished, l.id),
		ToolName:  bc.call.Name,
		Duration:  toolDuration.Round(time.Millisecond),
	})
	if toolResult.IsError {
		l.emit(ToolFailedEvent{
			BaseEvent: NewBaseEvent(EventToolFailed, l.id),
			ToolName:  bc.call.Name,
			Input:     bc.call.Input,
			Error:     toolResult.Output,
			Duration:  toolDuration.Round(time.Millisecond),
		})
	} else {
		l.emit(ToolSucceededEvent{
			BaseEvent: NewBaseEvent(EventToolSucceeded, l.id),
			ToolName:  bc.call.Name,
			Input:     bc.call.Input,
			Output:    toolResult.Output,
			Duration:  toolDuration.Round(time.Millisecond),
			Metadata:  toolResult.Metadata,
		})
	}
	return toolResult
}

func (l *Loop) emit(e Event) {
	if l.emitter != nil {
		l.emitter.Emit(e)
	}
}

// emitToolDenied fires the typed permission-denial signal for one blocked
// call (alongside the ToolFailedEvent the error result produces).
func (l *Loop) emitToolDenied(call ToolCall, reason string) {
	l.emit(ToolDeniedEvent{
		BaseEvent: NewBaseEvent(EventToolDenied, l.id),
		ToolName:  call.Name,
		Input:     call.Input,
		Reason:    reason,
	})
}

// RunTurn executes a single turn: user input -> agent plan/execute loop -> agent result
func (l *Loop) RunTurn(ctx context.Context, input string) (TurnResult, error) {
	return l.run(ctx, input, func(ctx context.Context) error {
		return l.context.AppendUser(ctx, input)
	})
}

// RunTurnWithImages executes a single turn whose user message carries image
// content blocks alongside the query text. Data is raw base64. The caller
// owns the vision-capability check — this layer sends whatever it is given.
func (l *Loop) RunTurnWithImages(ctx context.Context, input string, images []common.ImageSource) (TurnResult, error) {
	if len(images) == 0 {
		return l.RunTurn(ctx, input)
	}
	return l.run(ctx, input, func(ctx context.Context) error {
		// Text first, then image blocks, one message — the shape vision
		// APIs expect.
		blocks := common.NewUserMessage(input).Content
		for _, img := range images {
			blocks = append(blocks, common.NewImageContent(img.MediaType, img.Data))
		}
		return l.context.AppendUserContent(ctx, blocks)
	})
}

// run is the shared turn body; appendInput seeds the user message into the
// context store before the loop starts.
func (l *Loop) run(ctx context.Context, input string, appendInput func(context.Context) error) (TurnResult, error) {
	var loopErr error
	iteration := 0
	invalidFinalRetries := 0
	loopStart := time.Now()
	var cumInputTokens, cumOutputTokens int64

	if err := appendInput(ctx); err != nil {
		return TurnResult{}, fmt.Errorf("append input: %w", err)
	}

	// execute loop
	slog.Debug("loop started", "runner", l.id, "input", input)
	slog.Debug("system prompt", "runner", l.id, "prompt_len", len(l.sysPrompt), "prompt", l.sysPrompt)
	l.emit(TurnStartedEvent{
		BaseEvent: NewBaseEvent(EventTurnStarted, l.id),
		Query:     input,
	})
	l.emit(LoopStartedEvent{
		BaseEvent: NewBaseEvent(EventLoopStarted, l.id),
		Input:     input,
	})
	if err := l.fsm.TransitionStates(ctx, LoopTransitionReset); err != nil {
		return TurnResult{}, fmt.Errorf("fsm reset: %w", err)
	}
	// Snapshot the tool surface for this turn: dynamic sources are re-read
	// once per turn, and every reasoning call sees the same definitions.
	l.tools.BeginTurn(ctx)
	toolDefs := l.tools.Definitions()
	for {
		iteration++
		if err := ctx.Err(); err != nil {
			loopErr = fmt.Errorf("loop canceled: %w", err)
			break
		}

		if err := l.fsm.TransitionStates(ctx, LoopTransitionStartReasoning); err != nil {
			loopErr = fmt.Errorf("fsm start reasoning: %w", err)
			break
		}
		turnCtx := &TurnContext{
			RunnerID:     l.id,
			Iteration:    iteration,
			Elapsed:      time.Since(loopStart),
			InputTokens:  cumInputTokens,
			OutputTokens: cumOutputTokens,
		}
		if err := l.extensions.RunBeforeIteration(ctx, turnCtx); err != nil {
			loopErr = fmt.Errorf("before iteration hook: %w", err)
			break
		}
		if turnCtx.Terminate != "" {
			// graceful termination requested (budgets, Phase 5). Stop like a final answer.
			if err := l.fsm.TransitionStates(ctx, LoopTransitionReset); err != nil {
				slog.Error("fsm reset after termination", "runner", l.id, "error", err)
			}
			slog.Debug("loop terminated by hook", "runner", l.id, "reason", turnCtx.Terminate)
			return TurnResult{Terminated: turnCtx.Terminate}, nil
		}
		reminders := turnCtx.Reminders
		if len(reminders) > 0 {
			slog.Debug("system reminders", "runner", l.id, "iter", iteration, "count", len(reminders), "reminders", reminders)
		}
		l.emit(ReasoningStartedEvent{
			BaseEvent: NewBaseEvent(EventReasoningStarted, l.id),
			Iteration: iteration,
		})
		messages, err := l.context.Messages(ctx)
		if err != nil {
			loopErr = fmt.Errorf("read context: %w", err)
			break
		}
		reasoningResult, err := l.model.DoReasoning(ctx, messages, reminders, toolDefs)
		if err != nil {
			loopErr = fmt.Errorf("reasoning error: %w", err)
			break
		}
		cumInputTokens += reasoningResult.Meta.InputTokens
		cumOutputTokens += reasoningResult.Meta.OutputTokens
		l.emit(ReasoningFinishedEvent{
			BaseEvent:    NewBaseEvent(EventReasoningFinished, l.id),
			Model:        reasoningResult.Meta.Model,
			InputTokens:  reasoningResult.Meta.InputTokens,
			OutputTokens: reasoningResult.Meta.OutputTokens,
			StopReason:   reasoningResult.Meta.StopReason,
			HasToolCall:  len(reasoningResult.ToolCalls) > 0,
		})
		l.emit(LLMResponseEvent{
			BaseEvent:                NewBaseEvent(EventLLMResponse, l.id),
			Model:                    reasoningResult.Meta.Model,
			ResponseID:               reasoningResult.Meta.ResponseID,
			InputTokens:              reasoningResult.Meta.InputTokens,
			OutputTokens:             reasoningResult.Meta.OutputTokens,
			CacheReadInputTokens:     reasoningResult.Meta.CacheReadInputTokens,
			CacheCreationInputTokens: reasoningResult.Meta.CacheCreationInputTokens,
			StopReason:               reasoningResult.Meta.StopReason,
			Text:                     reasoningResult.Meta.AssistantText,
		})
		if len(reasoningResult.ToolCalls) > 0 {
			for _, tc := range reasoningResult.ToolCalls {
				slog.Debug("reasoning result", "runner", l.id, "iter", iteration, "tool", tc.Name, "tool_use_id", tc.ID, "input", tc.Input)
			}
		} else {
			slog.Debug("reasoning result", "runner", l.id, "iter", iteration, "final_answer_len", len(reasoningResult.FinalAnswer))
		}
		if err := l.fsm.TransitionStates(ctx, LoopTransitionFinishReasoning); err != nil {
			loopErr = fmt.Errorf("fsm finish reasoning: %w", err)
			break
		}

		if len(reasoningResult.ToolCalls) == 0 {
			if err := l.context.AppendAssistant(ctx, reasoningResult.Meta.AssistantMessage); err != nil {
				loopErr = fmt.Errorf("append assistant message: %w", err)
				break
			}
			finalAnswer := reasoningResult.FinalAnswer
			// max_tokens wins over the content checks: thinking can consume
			// the whole output cap and leave an empty answer, and the model
			// needs to hear "cut off", not "empty".
			truncated := reasoningResult.Meta.StopReason == string(common.StopReasonMaxTokens)
			reason := invalidFinalAnswerReason(finalAnswer)
			if truncated {
				reason = "the response was cut off by the output token limit before it finished"
			}
			if reason != "" && invalidFinalRetries < maxInvalidFinalRetries {
				invalidFinalRetries++
				slog.Warn("invalid final answer, retrying", "runner", l.id, "iter", iteration, "reason", reason, "answer_len", len(finalAnswer), "retry", invalidFinalRetries)
				if err := l.fsm.TransitionStates(ctx, LoopTransitionStartToolExecution); err != nil {
					loopErr = fmt.Errorf("fsm start retry after invalid final answer: %w", err)
					break
				}
				if err := l.fsm.TransitionStates(ctx, LoopTransitionFinishToolExecution); err != nil {
					loopErr = fmt.Errorf("fsm finish retry after invalid final answer: %w", err)
					break
				}
				retryMsg := fmt.Sprintf(
					"Your previous response was rejected: %s. If you intended to call a tool, use the tool-calling mechanism — never write a tool call as text. Otherwise, reply with your final answer as plain prose.",
					reason)
				if truncated {
					retryMsg = "Your previous response was cut off by the output token limit before it finished. Write the complete final answer again from the start, more concisely, so it fits within the limit."
				}
				if err := l.context.AppendUser(ctx, retryMsg); err != nil {
					loopErr = fmt.Errorf("append retry message: %w", err)
					break
				}
				continue
			}
			if reason != "" && strings.TrimSpace(finalAnswer) == "" {
				// Retries exhausted with nothing to show. An empty answer
				// reported as success is invisible to the caller; an error
				// reaches the UI.
				loopErr = fmt.Errorf("model produced no output after %d retries: %s", invalidFinalRetries, reason)
				break
			}
			if err := l.fsm.TransitionStates(ctx, LoopTransitionStop); err != nil {
				return TurnResult{}, fmt.Errorf("fsm stop: %w", err)
			}
			dur := time.Since(loopStart).Round(time.Millisecond)
			slog.Debug("loop completed", "runner", l.id, "iterations", iteration, "duration", dur, "answer_len", len(finalAnswer))
			slog.Debug("final answer", "runner", l.id, "answer", finalAnswer)
			l.emit(LoopStoppedEvent{
				BaseEvent:  NewBaseEvent(EventLoopStopped, l.id),
				Iterations: iteration,
				Duration:   dur,
			})
			l.emit(TurnCompletedEvent{
				BaseEvent:   NewBaseEvent(EventTurnCompleted, l.id),
				FinalAnswer: finalAnswer,
				Iterations:  iteration,
				Duration:    dur,
			})
			tr := TurnResult{
				FinalAnswer: finalAnswer,
				Iterations:  iteration,
				Duration:    dur,
			}
			l.extensions.RunAfterTurn(ctx, &tr)
			return tr, nil
		}

		if err := ctx.Err(); err != nil {
			loopErr = fmt.Errorf("loop canceled: %w", err)
			break
		}

		if err := l.fsm.TransitionStates(ctx, LoopTransitionStartToolExecution); err != nil {
			loopErr = fmt.Errorf("fsm start tool execution: %w", err)
			break
		}
		// Batch semantics: decisions run sequentially in issue order (hooks
		// and approval requests fire immediately, non-blocking); execution
		// runs in segments — consecutive read-only calls (per
		// ToolPort.ReadOnly) concurrently, bounded by
		// maxParallelReadOnlyTools, while mutating calls act as barriers and
		// run alone. Results land by index so feedback keeps issue order —
		// skipping any would poison the history with "not executed" results.
		// Post-hooks run sequentially in issue order after the barrier, and
		// the context store is only touched after it.
		pending := make([]batchCall, len(reasoningResult.ToolCalls))
		for i, toolCall := range reasoningResult.ToolCalls {
			tcc := &ToolCallContext{RunnerID: l.id, Call: &toolCall, Origin: l.tools.Origin(toolCall.Name)}
			hookErr := l.extensions.RunToolCall(ctx, tcc)
			bc := batchCall{call: *tcc.Call, decision: tcc.Decision, reason: tcc.Reason, hookErr: hookErr}
			if hookErr == nil && tcc.Decision == AskUser {
				if l.skipPermissions {
					// Dangerously-skip-permissions mode: auto-approve
					// without emitting an approval request. Deny still
					// denies.
					bc.decision = Allow
				} else {
					bc.approval = l.requestApproval(bc.call, bc.reason)
				}
			}
			pending[i] = bc
		}

		outputs := l.executeSegmented(ctx, iteration, pending)

		for i := range outputs {
			l.extensions.RunToolResult(ctx, &ToolResultContext{RunnerID: l.id, Call: pending[i].call, Result: &outputs[i]})
		}
		if err := l.fsm.TransitionStates(ctx, LoopTransitionFinishToolExecution); err != nil {
			loopErr = fmt.Errorf("fsm finish tool execution: %w", err)
			break
		}

		// Feed the assistant's tool_use turn and the tool results back into
		// the context store; the next iteration rebuilds messages from it.
		if err := l.context.AppendAssistant(ctx, reasoningResult.Meta.AssistantMessage); err != nil {
			loopErr = fmt.Errorf("append assistant message: %w", err)
			break
		}
		if err := l.context.AppendToolResults(ctx, outputs); err != nil {
			loopErr = fmt.Errorf("append tool results: %w", err)
			break
		}

		// Steering: user messages submitted mid-turn are appended after the
		// tool results, so tool_use/tool_result pairing in the context store
		// is never split by an interleaved user message.
		for _, msg := range l.drainSteering() {
			slog.Debug("steering injected", "runner", l.id, "iter", iteration, "message_len", len(msg))
			if err := l.context.AppendUser(ctx, msg); err != nil {
				loopErr = fmt.Errorf("append steering message: %w", err)
				break
			}
			l.emit(SteeringInjectedEvent{
				BaseEvent: NewBaseEvent(EventSteeringInjected, l.id),
				Message:   msg,
			})
		}
		if loopErr != nil {
			break
		}
	}

	if err := l.fsm.TransitionStates(ctx, LoopTransitionReset); err != nil {
		slog.Error("fsm reset after error", "runner", l.id, "error", err)
	}
	// A canceled context means the caller stopped the turn — that is an
	// outcome, not a failure. Cancellation can surface from any ctx-aware
	// call (the model, a tool, a context-store write), so it is classified
	// once here rather than at every call site, and reported without the
	// ErrorEvent and error-level log a genuine fault gets. A deadline
	// (wall-clock budget, --timeout) is left as a failure: the turn ran out
	// of room rather than being called off.
	if errors.Is(loopErr, context.Canceled) {
		slog.Info("loop canceled", "runner", l.id, "iterations", iteration,
			"duration", time.Since(loopStart).Round(time.Millisecond), "at", loopErr)
		return TurnResult{}, fmt.Errorf("turn canceled: %w", context.Canceled)
	}

	slog.Error("loop failed", "runner", l.id, "error", loopErr, "iterations", iteration, "duration", time.Since(loopStart).Round(time.Millisecond))
	l.emit(ErrorEvent{
		BaseEvent: NewBaseEvent(EventError, l.id),
		Error:     loopErr.Error(),
		Context:   "loop",
	})
	return TurnResult{}, loopErr
}
