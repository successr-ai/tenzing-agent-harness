package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/looplab/fsm"
)

// LevelTrace is below slog.LevelDebug (-4). Filtered out by default;
// set handler level to LevelTrace or lower to see these.
const LevelTrace = slog.Level(-8)

// LoopState is the current state of the reasoning-tool execution loop.
type LoopState string

func (s LoopState) String() string { return string(s) }

// LoopTransition is a transition between two LoopStates.
type LoopTransition string

func (t LoopTransition) String() string { return string(t) }

const (
	LoopStateStarted               LoopState = "started"
	LoopStateStopped               LoopState = "stopped"
	LoopStateReasoningStarted      LoopState = "reasoning_started"
	LoopStateReasoningFinished     LoopState = "reasoning_finished"
	LoopStateToolExecutionStarted  LoopState = "tool_execution_started"
	LoopStateToolExecutionFinished LoopState = "tool_execution_finished"

	LoopTransitionStartReasoning      LoopTransition = "start_reasoning"
	LoopTransitionFinishReasoning     LoopTransition = "finish_reasoning"
	LoopTransitionStartToolExecution  LoopTransition = "start_tool_execution"
	LoopTransitionFinishToolExecution LoopTransition = "finish_tool_execution"
	LoopTransitionStop                LoopTransition = "stop"
	LoopTransitionReset               LoopTransition = "reset"
)

// LoopFSM is the finite state machine that defines the allowed transitions between loop states.
type LoopFSM struct {
	*fsm.FSM
}

// TransitionStates applies a transition, reporting nil when the machine is
// already in the destination state.
//
// Cancellation is stripped from ctx first. A transition is in-memory
// bookkeeping with no I/O to abort, and looplab/fsm abandons one mid-flight
// when the context is done: it leaves the pending transition set without
// advancing the state, so every later transition — the loop's terminal
// reset included — fails with "previous transition did not complete", and
// re-running it cannot help because it re-checks the same dead context.
// That wedges the loop for the rest of the process. Context values (trace
// IDs, logger state) are preserved.
func (f *LoopFSM) TransitionStates(ctx context.Context, transition LoopTransition) error {
	ctx = context.WithoutCancel(ctx)
	err := f.Event(ctx, string(transition))
	if err != nil {
		var noTransition fsm.NoTransitionError
		if errors.As(err, &noTransition) {
			return nil
		}
		return fmt.Errorf("unable to set loop state to %s: %w", transition, err)
	}
	slog.Log(ctx, LevelTrace, fmt.Sprintf("loop state is now %s", f.Current()))
	return nil
}

func NewLoopFSM() *LoopFSM {
	f := fsm.NewFSM(
		string(LoopStateStarted),
		fsm.Events{
			{Name: LoopTransitionStartReasoning.String(), Src: toStrings(LoopStateStarted, LoopStateToolExecutionFinished), Dst: LoopStateReasoningStarted.String()},
			{Name: LoopTransitionFinishReasoning.String(), Src: toStrings(LoopStateReasoningStarted), Dst: LoopStateReasoningFinished.String()},
			{Name: LoopTransitionStartToolExecution.String(), Src: toStrings(LoopStateReasoningFinished), Dst: LoopStateToolExecutionStarted.String()},
			{Name: LoopTransitionFinishToolExecution.String(), Src: toStrings(LoopStateToolExecutionStarted), Dst: LoopStateToolExecutionFinished.String()},
			{Name: LoopTransitionStop.String(), Src: toStrings(LoopStateReasoningFinished), Dst: LoopStateStopped.String()},
			{
				Name: LoopTransitionReset.String(),
				Src: toStrings(
					LoopStateStarted,
					LoopStateReasoningStarted,
					LoopStateReasoningFinished,
					LoopStateToolExecutionStarted,
					LoopStateToolExecutionFinished,
					LoopStateStopped,
				),
				Dst: LoopStateStarted.String(),
			},
		},
		fsm.Callbacks{},
	)
	return &LoopFSM{FSM: f}
}

func toStrings[T fmt.Stringer](vs ...T) []string {
	strs := make([]string, 0, len(vs))
	for _, v := range vs {
		strs = append(strs, v.String())
	}
	return strs
}
