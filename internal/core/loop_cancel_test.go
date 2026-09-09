package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// cancelingTools cancels the turn from inside a tool call — what actually
// happens when a user hits cancel while a tool is running.
type cancelingTools struct {
	cancel context.CancelFunc
}

func (c *cancelingTools) BeginTurn(_ context.Context)          {}
func (c *cancelingTools) Definitions() []common.ToolDefinition { return nil }
func (c *cancelingTools) Origin(_ string) string               { return "native" }
func (c *cancelingTools) ReadOnly(_ string) bool               { return false }

func (c *cancelingTools) Execute(_ context.Context, call ToolCall) ToolResult {
	c.cancel()
	return ToolResult{ToolUseID: call.ID, Output: "interrupted"}
}

// TestCancelDuringToolExecution pins the contract for a user-requested
// stop: it reports as a cancellation, it leaves the FSM reusable, and it
// does not masquerade as a loop failure.
func TestCancelDuringToolExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := &fakeModel{steps: []ReasoningResult{toolCallResult(ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"sleep 60"}`})}}
	emitter := &fakeEmitter{}
	l := newTestLoop(t, model, &cancelingTools{cancel: cancel}, newFakeContext(),
		func(cfg *LoopConfig) { cfg.Emitter = emitter })

	_, err := l.RunTurn(ctx, "do the thing")

	if err == nil {
		t.Fatal("RunTurn returned no error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %q does not unwrap to context.Canceled", err)
	}
	// The FSM error text is the symptom the user saw; a cancel should never
	// be described as a state-machine failure.
	if got := err.Error(); !strings.Contains(got, "cancel") || strings.Contains(got, "fsm ") {
		t.Errorf("error %q should read as a cancellation, not an FSM failure", got)
	}

	// The loop must be reusable: the terminal reset cannot be skipped just
	// because the turn's context is dead, or the harness reports busy
	// forever and /clear, /compact and model switching all fail.
	if got := l.fsm.Current(); got != string(LoopStateStarted) {
		t.Errorf("loop state = %q after cancellation, want %q", got, LoopStateStarted)
	}

	for _, ev := range emitter.eventTypes() {
		if ev == EventError {
			t.Error("cancellation emitted an ErrorEvent; a user-requested stop is not a failure")
		}
	}
}

// TestFSMSurvivesCanceledContext pins the reason TransitionStates strips
// cancellation: looplab/fsm abandons a transition whose context is done
// without clearing the pending one, and every later transition — including
// the loop's terminal reset — then fails with "previous transition did not
// complete". Re-running it cannot recover, because it re-checks the same
// dead context.
func TestFSMSurvivesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := NewLoopFSM()
	if err := f.TransitionStates(ctx, LoopTransitionStartReasoning); err != nil {
		t.Fatalf("transition with a canceled context: %v", err)
	}
	if got := f.Current(); got != string(LoopStateReasoningStarted) {
		t.Fatalf("state = %q, want %q — the transition was abandoned", got, LoopStateReasoningStarted)
	}

	// The machine must remain usable afterwards.
	if err := f.TransitionStates(ctx, LoopTransitionFinishReasoning); err != nil {
		t.Fatalf("follow-up transition: %v", err)
	}
	if err := f.TransitionStates(ctx, LoopTransitionReset); err != nil {
		t.Fatalf("terminal reset: %v", err)
	}
	if got := f.Current(); got != string(LoopStateStarted) {
		t.Errorf("state = %q after reset, want %q", got, LoopStateStarted)
	}
}

// TestDeadlineStillFails guards the line between the two: a wall-clock
// budget running out is a failure, not a cancellation, and must keep its
// ErrorEvent.
func TestDeadlineStillFails(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	model := &fakeModel{steps: []ReasoningResult{{FinalAnswer: "unreachable"}}}
	emitter := &fakeEmitter{}
	l := newTestLoop(t, model, newFakeTools(nil), newFakeContext(),
		func(cfg *LoopConfig) { cfg.Emitter = emitter })

	_, err := l.RunTurn(ctx, "do the thing")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a deadline error", err)
	}
	if strings.Contains(err.Error(), "turn canceled") {
		t.Errorf("deadline reported as a cancellation: %q", err)
	}
	var sawError bool
	for _, ev := range emitter.eventTypes() {
		if ev == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("deadline did not emit an ErrorEvent; a budget overrun is still a failure")
	}
}
