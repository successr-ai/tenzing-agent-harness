package budgets

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func run(t *testing.T, l Limits, tc *core.TurnContext) *core.TurnContext {
	t.Helper()
	if err := New(l).BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("BeforeIteration: %v", err)
	}
	return tc
}

func TestIterationBudget(t *testing.T) {
	l := Limits{MaxIterations: 3}
	if tc := run(t, l, &core.TurnContext{Iteration: 3}); tc.Terminate != "" {
		t.Errorf("iteration 3 of 3 must run, got terminate %q", tc.Terminate)
	}
	tc := run(t, l, &core.TurnContext{Iteration: 4})
	if tc.Terminate == "" || !strings.Contains(tc.Terminate, "iteration") {
		t.Errorf("iteration 4 of 3 must terminate, got %q", tc.Terminate)
	}
}

func TestWallClockBudget(t *testing.T) {
	l := Limits{MaxWallClock: time.Minute}
	if tc := run(t, l, &core.TurnContext{Iteration: 1, Elapsed: 59 * time.Second}); tc.Terminate != "" {
		t.Errorf("under wall clock must run, got %q", tc.Terminate)
	}
	tc := run(t, l, &core.TurnContext{Iteration: 2, Elapsed: 61 * time.Second})
	if tc.Terminate == "" || !strings.Contains(tc.Terminate, "wall") {
		t.Errorf("over wall clock must terminate, got %q", tc.Terminate)
	}
}

func TestTokenBudget(t *testing.T) {
	l := Limits{MaxTokens: 1000}
	if tc := run(t, l, &core.TurnContext{Iteration: 1, InputTokens: 400, OutputTokens: 400}); tc.Terminate != "" {
		t.Errorf("under token budget must run, got %q", tc.Terminate)
	}
	tc := run(t, l, &core.TurnContext{Iteration: 2, InputTokens: 600, OutputTokens: 500})
	if tc.Terminate == "" || !strings.Contains(tc.Terminate, "token") {
		t.Errorf("over token budget must terminate, got %q", tc.Terminate)
	}
}

func TestZeroLimitsNeverTerminate(t *testing.T) {
	tc := run(t, Limits{}, &core.TurnContext{
		Iteration:    10_000,
		Elapsed:      100 * time.Hour,
		InputTokens:  1 << 40,
		OutputTokens: 1 << 40,
	})
	if tc.Terminate != "" {
		t.Errorf("zero limits are unlimited, got %q", tc.Terminate)
	}
}
