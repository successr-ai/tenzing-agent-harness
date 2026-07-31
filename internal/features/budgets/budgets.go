// Package budgets terminates a turn gracefully when it exceeds configured
// limits. A core.BeforeIterationHook: the loop populates TurnContext with
// the turn's elapsed time and cumulative token usage before each iteration;
// this extension sets Terminate when any limit is exceeded, and the loop
// returns TurnResult{Terminated: reason} — a result, not an error.
//
// Cost budget (USD) is a non-goal for now: the provider layer exposes no pricing
// tables to convert tokens into dollars.
package budgets

import (
	"context"
	"fmt"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Limits are per-turn budgets. A zero field means unlimited.
type Limits struct {
	MaxIterations int
	MaxWallClock  time.Duration
	MaxTokens     int64 // input + output, cumulative for the turn
}

var (
	_ core.Extension           = (*Ext)(nil)
	_ core.BeforeIterationHook = (*Ext)(nil)
)

type Ext struct {
	limits Limits
}

func New(l Limits) *Ext { return &Ext{limits: l} }

func (e *Ext) Name() string { return "budgets" }

func (e *Ext) BeforeIteration(_ context.Context, tc *core.TurnContext) error {
	l := e.limits
	switch {
	case l.MaxIterations > 0 && tc.Iteration > l.MaxIterations:
		tc.Terminate = fmt.Sprintf("iteration budget exhausted (%d iterations)", l.MaxIterations)
	case l.MaxWallClock > 0 && tc.Elapsed >= l.MaxWallClock:
		tc.Terminate = fmt.Sprintf("wall-clock budget exhausted (%s)", l.MaxWallClock)
	case l.MaxTokens > 0 && tc.InputTokens+tc.OutputTokens >= l.MaxTokens:
		tc.Terminate = fmt.Sprintf("token budget exhausted (%d tokens)", l.MaxTokens)
	}
	return nil
}
