package advisor

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// testGate returns a gate whose classifier marks read/grep as read-only.
func testGate(nudge int) *GateExt {
	g := NewGateExt(GateConfig{NudgeIteration: nudge})
	g.SetClassifier(func(name string) bool {
		return name == "read" || name == "grep"
	})
	return g
}

func call(t *testing.T, g *GateExt, tool string) *core.ToolCallContext {
	t.Helper()
	tcc := &core.ToolCallContext{Call: &core.ToolCall{Name: tool}}
	if err := g.OnToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("OnToolCall(%s): %v", tool, err)
	}
	return tcc
}

func startTurn(t *testing.T, g *GateExt, iteration int) *core.TurnContext {
	t.Helper()
	tc := &core.TurnContext{Iteration: iteration}
	if err := g.BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("BeforeIteration: %v", err)
	}
	return tc
}

func TestGateExt_Matrix(t *testing.T) {
	tests := []struct {
		name       string
		sequence   []string // tools called in order; last one is asserted
		want       core.Decision
		wantReason string
	}{
		{"read-only before consult", []string{"read"}, core.Allow, ""},
		{"write before consult", []string{"write"}, core.Deny, "advisor"},
		{"unknown tool before consult", []string{"mcp__srv__thing"}, core.Deny, "advisor"},
		{"advisor call", []string{"advisor"}, core.Allow, ""},
		{"write after consult", []string{"advisor", "write"}, core.Allow, ""},
		{"bash after read then consult", []string{"read", "advisor", "bash"}, core.Allow, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testGate(0)
			startTurn(t, g, 1)
			var last *core.ToolCallContext
			for _, tool := range tt.sequence {
				last = call(t, g, tool)
			}
			if last.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason %q)", last.Decision, tt.want, last.Reason)
			}
			if tt.wantReason != "" && !strings.Contains(last.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to mention %q", last.Reason, tt.wantReason)
			}
		})
	}
}

// testGateWithExempt is testGate plus WithAdvisorExemptTools-style names.
func testGateWithExempt(nudge int, exempt ...string) *GateExt {
	g := NewGateExt(GateConfig{NudgeIteration: nudge, ExemptTools: exempt})
	g.SetClassifier(func(name string) bool {
		return name == "read" || name == "grep"
	})
	return g
}

func TestGateExt_ExemptTools(t *testing.T) {
	tests := []struct {
		name     string
		sequence []string
		want     core.Decision
	}{
		{"exempt tool before consult", []string{"propose_grid"}, core.Allow},
		{"exempt tool matched case-insensitively", []string{"Propose_Grid"}, core.Allow},
		{"non-exempt write before consult still denied", []string{"write"}, core.Deny},
		{"exempt tool does not itself count as a consult", []string{"propose_grid", "write"}, core.Deny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testGateWithExempt(0, "propose_grid")
			startTurn(t, g, 1)
			var last *core.ToolCallContext
			for _, tool := range tt.sequence {
				last = call(t, g, tool)
			}
			if last.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason %q)", last.Decision, tt.want, last.Reason)
			}
		})
	}
}

// A gate with no exempt tools configured behaves exactly as before: this
// guards against the variadic default silently changing behavior.
func TestGateExt_NoExemptToolsUnchanged(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	if tcc := call(t, g, "write"); tcc.Decision != core.Deny {
		t.Errorf("write before consult, no exemptions = %v, want Deny", tcc.Decision)
	}
}

func TestGateExt_ExemptToolNeverLowersDecision(t *testing.T) {
	g := testGateWithExempt(0, "propose_grid")
	startTurn(t, g, 1)
	tcc := &core.ToolCallContext{
		Call:     &core.ToolCall{Name: "propose_grid"},
		Decision: core.Deny,
		Reason:   "denied earlier",
	}
	if err := g.OnToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("OnToolCall: %v", err)
	}
	if tcc.Decision != core.Deny || tcc.Reason != "denied earlier" {
		t.Errorf("exemption overwrote a prior Deny: %v %q", tcc.Decision, tcc.Reason)
	}
}

func TestGateExt_ResetsPerTurn(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	call(t, g, "advisor")
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Fatalf("write after consult = %v, want Allow", tcc.Decision)
	}

	startTurn(t, g, 1) // new turn
	if tcc := call(t, g, "write"); tcc.Decision != core.Deny {
		t.Errorf("write on fresh turn = %v, want Deny", tcc.Decision)
	}
}

func TestGateExt_MidTurnIterationKeepsState(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	call(t, g, "advisor")
	startTurn(t, g, 2) // later iteration of the same turn: no reset
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Errorf("write at iteration 2 = %v, want Allow (consult persists)", tcc.Decision)
	}
}

func TestGateExt_CallCap(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	for i := range DefaultMaxCallsPerTurn {
		if tcc := call(t, g, "advisor"); tcc.Decision != core.Allow {
			t.Fatalf("advisor call %d = %v, want Allow", i+1, tcc.Decision)
		}
	}
	tcc := call(t, g, "advisor")
	if tcc.Decision != core.Deny {
		t.Fatalf("advisor call beyond cap = %v, want Deny", tcc.Decision)
	}
	if !strings.Contains(tcc.Reason, "cap") {
		t.Errorf("Reason = %q, want cap mention", tcc.Reason)
	}
	// The capped turn still counts as consulted: writes stay allowed.
	if w := call(t, g, "write"); w.Decision != core.Allow {
		t.Errorf("write after capped consults = %v, want Allow", w.Decision)
	}
}

func TestGateExt_NeverLowersDecision(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	tcc := &core.ToolCallContext{
		Call:     &core.ToolCall{Name: "read"},
		Decision: core.Deny,
		Reason:   "denied earlier",
	}
	if err := g.OnToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("OnToolCall: %v", err)
	}
	if tcc.Decision != core.Deny || tcc.Reason != "denied earlier" {
		t.Errorf("gate lowered/overwrote a prior Deny: %v %q", tcc.Decision, tcc.Reason)
	}
}

func TestGateExt_NilClassifierFailsClosed(t *testing.T) {
	g := NewGateExt(GateConfig{}) // classifier never bound
	startTurn(t, g, 1)
	if tcc := call(t, g, "read"); tcc.Decision != core.Deny {
		t.Errorf("read with nil classifier = %v, want Deny (fail closed)", tcc.Decision)
	}
}

func TestGateExt_Nudge(t *testing.T) {
	tests := []struct {
		name      string
		nudge     int
		iteration int
		consult   bool
		want      bool
	}{
		{"disabled", 0, 5, false, false},
		{"before threshold", 3, 2, false, false},
		{"at threshold unconsulted", 3, 3, false, true},
		{"after threshold unconsulted", 3, 7, false, true},
		{"at threshold consulted", 3, 3, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testGate(tt.nudge)
			startTurn(t, g, 1)
			if tt.consult {
				call(t, g, "advisor")
			}
			tc := startTurn(t, g, tt.iteration)
			got := len(tc.Reminders) > 0
			if got != tt.want {
				t.Errorf("reminder present = %v, want %v (reminders: %v)", got, tt.want, tc.Reminders)
			}
		})
	}
}

func TestGateExt_PromptFragment(t *testing.T) {
	g := NewGateExt(GateConfig{})
	frag := g.PromptFragment()
	if !strings.Contains(frag, "advisor") {
		t.Errorf("prompt fragment does not mention advisor: %q", frag)
	}
	if !strings.Contains(frag, "state-changing") {
		t.Errorf("prompt fragment does not state the hard rule: %q", frag)
	}
	if !strings.Contains(frag, "TodoUpdate") {
		t.Errorf("prompt fragment does not state the plan-checkpoint rule: %q", frag)
	}
	if !strings.Contains(frag, "after 6 loop iterations") {
		t.Errorf("prompt fragment does not state the default cadence rule: %q", frag)
	}
	if off := NewGateExt(GateConfig{Cadence: -1}).PromptFragment(); strings.Contains(off, "Cadence") {
		t.Errorf("prompt fragment mentions cadence with the rule disabled: %q", off)
	}
}

func TestGateExt_ConfigDefaults(t *testing.T) {
	tests := []struct {
		name         string
		cfg          GateConfig
		wantCadence  int
		wantMaxCalls int
	}{
		{"zero config", GateConfig{}, DefaultCadence, DefaultMaxCallsPerTurn},
		{"explicit", GateConfig{Cadence: 3, MaxCallsPerTurn: 5}, 3, 5},
		{"cadence off", GateConfig{Cadence: -1}, -1, DefaultMaxCallsPerTurn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGateExt(tt.cfg)
			if g.cadence != tt.wantCadence || g.maxCalls != tt.wantMaxCalls {
				t.Errorf("cadence=%d maxCalls=%d, want %d %d", g.cadence, g.maxCalls, tt.wantCadence, tt.wantMaxCalls)
			}
		})
	}
}

func TestGateExt_MaxCallsConfigurable(t *testing.T) {
	g := NewGateExt(GateConfig{MaxCallsPerTurn: 2})
	g.SetClassifier(func(string) bool { return false })
	startTurn(t, g, 1)
	call(t, g, "advisor")
	call(t, g, "advisor")
	if tcc := call(t, g, "advisor"); tcc.Decision != core.Deny {
		t.Errorf("third advisor call with cap 2 = %v, want Deny", tcc.Decision)
	}
}

// cadenceGate returns a gate with cadence 3 whose classifier marks read as
// read-only, consulted at iteration 1.
func cadenceGate(t *testing.T, cadence int) *GateExt {
	t.Helper()
	g := NewGateExt(GateConfig{Cadence: cadence})
	g.SetClassifier(func(name string) bool { return name == "read" })
	startTurn(t, g, 1)
	call(t, g, "advisor")
	return g
}

func TestGateExt_Cadence(t *testing.T) {
	tests := []struct {
		name       string
		cadence    int
		iteration  int    // iteration reached after the consult at 1
		tool       string // asserted call at that iteration
		want       core.Decision
		wantRemind bool
	}{
		{"read below cadence", 3, 3, "read", core.Allow, false},
		{"write below cadence", 3, 3, "write", core.Allow, false},
		{"read at cadence blocked", 3, 4, "read", core.Deny, true},
		{"write at cadence blocked", 3, 4, "write", core.Deny, true},
		{"well past cadence blocked", 3, 9, "read", core.Deny, true},
		{"advisor at cadence allowed", 3, 4, "advisor", core.Allow, true},
		{"cadence disabled", -1, 50, "write", core.Allow, false},
		{"default cadence below", 0, 6, "write", core.Allow, false},
		{"default cadence at", 0, 7, "write", core.Deny, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := cadenceGate(t, tt.cadence)
			var tc *core.TurnContext
			for i := 2; i <= tt.iteration; i++ {
				tc = startTurn(t, g, i)
			}
			if got := tc != nil && len(tc.Reminders) > 0; got != tt.wantRemind {
				t.Errorf("reminder present = %v, want %v", got, tt.wantRemind)
			}
			if tcc := call(t, g, tt.tool); tcc.Decision != tt.want {
				t.Errorf("%s at iteration %d = %v, want %v (%q)", tt.tool, tt.iteration, tcc.Decision, tt.want, tcc.Reason)
			}
		})
	}
}

func TestGateExt_CadenceResetsOnConsult(t *testing.T) {
	g := cadenceGate(t, 3)
	for i := 2; i <= 4; i++ {
		startTurn(t, g, i)
	}
	if tcc := call(t, g, "write"); tcc.Decision != core.Deny {
		t.Fatalf("write at cadence = %v, want Deny", tcc.Decision)
	}
	call(t, g, "advisor")
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Errorf("write right after re-consult = %v, want Allow (%q)", tcc.Decision, tcc.Reason)
	}
	// counter restarted: two more iterations still under cadence
	startTurn(t, g, 5)
	startTurn(t, g, 6)
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Errorf("write 2 iterations after re-consult = %v, want Allow", tcc.Decision)
	}
}

func TestGateExt_CadenceResetsPerTurn(t *testing.T) {
	g := cadenceGate(t, 3)
	for i := 2; i <= 4; i++ {
		startTurn(t, g, i)
	}
	startTurn(t, g, 1) // new turn
	call(t, g, "advisor")
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Errorf("write on fresh turn after consult = %v, want Allow (%q)", tcc.Decision, tcc.Reason)
	}
}

// Once the call cap is reached the cadence rule yields, so a long turn cannot
// deadlock behind a consult the cap makes impossible.
func TestGateExt_CadenceYieldsAtCap(t *testing.T) {
	g := NewGateExt(GateConfig{Cadence: 2, MaxCallsPerTurn: 1})
	g.SetClassifier(func(string) bool { return false })
	startTurn(t, g, 1)
	call(t, g, "advisor") // 1 of 1
	for i := 2; i <= 4; i++ {
		if tc := startTurn(t, g, i); len(tc.Reminders) > 0 {
			t.Errorf("iteration %d: cadence reminder issued although cap reached", i)
		}
	}
	if tcc := call(t, g, "write"); tcc.Decision != core.Allow {
		t.Errorf("write past cadence with cap reached = %v, want Allow (%q)", tcc.Decision, tcc.Reason)
	}
}

func callInput(t *testing.T, g *GateExt, tool, input string) *core.ToolCallContext {
	t.Helper()
	tcc := &core.ToolCallContext{Call: &core.ToolCall{Name: tool, Input: input}}
	if err := g.OnToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("OnToolCall(%s): %v", tool, err)
	}
	return tcc
}

func TestGateExt_PlanCheckpoints(t *testing.T) {
	type step struct{ tool, input string }
	done := `{"id":"a1","status":"done"}`
	inProgress := `{"id":"a1","status":"in_progress"}`
	tests := []struct {
		name     string
		sequence []step // last one is asserted
		want     core.Decision
	}{
		{"TodoWrite right after consult", []step{{"advisor", ""}, {"TodoWrite", ""}}, core.Allow},
		{"TodoWrite after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoWrite", ""}}, core.Deny},
		{"TodoCreate after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoCreate", ""}}, core.Deny},
		{"TodoUpdate done after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoUpdate", done}}, core.Deny},
		{"TodoUpdate in_progress after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoUpdate", inProgress}}, core.Allow},
		{"TodoUpdate garbage input after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoUpdate", "{"}}, core.Allow},
		{"re-consult clears the debt", []step{{"advisor", ""}, {"write", ""}, {"advisor", ""}, {"TodoUpdate", done}}, core.Allow},
		{"todo tools do not count as writes", []step{{"advisor", ""}, {"TodoWrite", ""}, {"TodoUpdate", inProgress}, {"TodoUpdate", done}}, core.Allow},
		{"TodoRead after a write", []step{{"advisor", ""}, {"write", ""}, {"TodoRead", ""}}, core.Allow},
		{"plain write after a write", []step{{"advisor", ""}, {"write", ""}, {"edit", ""}}, core.Allow},
		{"TodoWrite before any consult", []step{{"TodoWrite", ""}}, core.Deny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testGate(0)
			startTurn(t, g, 1)
			var last *core.ToolCallContext
			for _, s := range tt.sequence {
				last = callInput(t, g, s.tool, s.input)
			}
			if last.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason %q)", last.Decision, tt.want, last.Reason)
			}
			if tt.want == core.Deny && !strings.Contains(last.Reason, "advisor") {
				t.Errorf("Reason = %q, want advisor mention", last.Reason)
			}
		})
	}
}

// A capped advisor call still clears checkpoint debt: the plan tools must
// not deadlock behind a consult the cap makes impossible.
func TestGateExt_CapClearsCheckpointDebt(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	for range DefaultMaxCallsPerTurn {
		call(t, g, "advisor")
	}
	call(t, g, "write")
	if tcc := callInput(t, g, "TodoUpdate", `{"status":"done"}`); tcc.Decision != core.Deny {
		t.Fatalf("checkpoint with debt = %v, want Deny", tcc.Decision)
	}
	call(t, g, "advisor") // capped, but still clears debt
	if tcc := callInput(t, g, "TodoUpdate", `{"status":"done"}`); tcc.Decision != core.Allow {
		t.Errorf("checkpoint after capped consult = %v, want Allow (%q)", tcc.Decision, tcc.Reason)
	}
}

func TestGateExt_ResetsCheckpointDebtPerTurn(t *testing.T) {
	g := testGate(0)
	startTurn(t, g, 1)
	call(t, g, "advisor")
	call(t, g, "write")
	startTurn(t, g, 1) // new turn
	call(t, g, "advisor")
	if tcc := call(t, g, "TodoWrite"); tcc.Decision != core.Allow {
		t.Errorf("TodoWrite after consult on fresh turn = %v, want Allow (%q)", tcc.Decision, tcc.Reason)
	}
}
