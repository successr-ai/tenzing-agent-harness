package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// maxAdvisorCallsPerTurn bounds consults per turn so a looping executor
// cannot burn advisor tokens indefinitely. Sized for the plan-checkpoint
// rule: one consult per plan write plus one per task marked done.
const maxAdvisorCallsPerTurn = 20

const gatePromptFragment = "## Advisor\n\n" +
	"Call `advisor` before substantive work — before writing, editing, or committing " +
	"to an approach. Orientation first (finding files, reading what's there) is fine " +
	"and encouraged; then consult. Also call it when stuck (recurring errors, an " +
	"approach that isn't converging), when torn between approaches — consult rather " +
	"than deliberating at length on your own — and once before declaring a " +
	"non-trivial task done. Give the advice serious weight; if your evidence " +
	"contradicts it, surface the " +
	"conflict in one more advisor call rather than silently switching. The advisor may " +
	"name a milestone to check back at; honor it.\n\n" +
	"Hard rules (enforced):\n" +
	"- Your first state-changing tool call each turn must be preceded by an advisor call. " +
	"Read-only orientation is always allowed first. This applies to one-line edits too.\n" +
	"- Plan checkpoints: `TodoWrite`, `TodoCreate`, and `TodoUpdate` with status `done` " +
	"require a fresh advisor call — one with no state-changing tool calls since. " +
	"Consult, then write or change the plan; finish a task, consult, then mark it done."

const nudgeReminder = "You have not consulted the advisor yet. If the task has a " +
	"non-obvious design decision or a failure mode you haven't ruled out, call " +
	"advisor now before committing to an approach."

var (
	_ core.Extension           = (*GateExt)(nil)
	_ core.ToolCallHook        = (*GateExt)(nil)
	_ core.BeforeIterationHook = (*GateExt)(nil)
	_ core.PromptContributor   = (*GateExt)(nil)
)

// GateExt enforces the advisor "hard rules" on the main loop. Per turn, the
// first state-changing tool call is denied until the advisor has been called.
// Plan checkpoints (TodoWrite, TodoCreate, TodoUpdate to done) are denied
// unless the advisor has been called since the last state-changing tool call,
// so the advisor reviews at every plan change and task boundary. Todo tools
// themselves never count as state-changing for that rule, so status flips
// between checkpoints flow freely. Read-only tools flow freely; unknown tools
// (no read-only marker, e.g. MCP) count as state-changing. Tools named via
// WithAdvisorExemptTools also flow freely, unconsulted — for harnesses whose
// first (and only) tool call is a forced/schema-only answer with no
// orientation phase to hide behind.
// classify is late-bound like readOnlyExt's: the composite ToolPort is built
// after the extension set, so harness.New assigns it via SetClassifier before
// any turn runs.
//
// nudgeIteration, when > 0, appends a reminder from that iteration onward on
// turns where the advisor has not been consulted (off by default — Anthropic
// measured nudging counterproductive on strong executor models).
type GateExt struct {
	nudgeIteration int
	exempt         map[string]bool

	mu                 sync.Mutex
	classify           func(name string) bool // true = read-only
	consulted          bool
	calls              int
	writesSinceConsult int
}

// NewGateExt builds the write-gate. nudgeIteration <= 0 disables the nudge.
// exemptTools names tools that bypass the gate even unconsulted (e.g. a
// harness's forced single-shot answer tool, which has no orientation phase
// to precede it).
func NewGateExt(nudgeIteration int, exemptTools ...string) *GateExt {
	var exempt map[string]bool
	if len(exemptTools) > 0 {
		exempt = make(map[string]bool, len(exemptTools))
		for _, name := range exemptTools {
			exempt[strings.ToLower(name)] = true
		}
	}
	return &GateExt{nudgeIteration: nudgeIteration, exempt: exempt}
}

// SetClassifier late-binds the read-only classifier (composite.ReadOnly).
func (e *GateExt) SetClassifier(classify func(name string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.classify = classify
}

func (e *GateExt) Name() string { return "advisor-gate" }

func (e *GateExt) PromptFragment() string { return gatePromptFragment }

// BeforeIteration resets the per-turn state on the first iteration and, when
// nudging is enabled, reminds an unconsulted executor to call the advisor.
func (e *GateExt) BeforeIteration(_ context.Context, tc *core.TurnContext) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tc.Iteration <= 1 {
		e.consulted = false
		e.calls = 0
		e.writesSinceConsult = 0
	}
	if e.nudgeIteration > 0 && tc.Iteration >= e.nudgeIteration && !e.consulted {
		tc.Reminders = append(tc.Reminders, nudgeReminder)
	}
	return nil
}

func (e *GateExt) OnToolCall(_ context.Context, tcc *core.ToolCallContext) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	name := strings.ToLower(tcc.Call.Name)
	switch {
	case name == "advisor":
		// A capped turn still counts as consulted and clears the checkpoint
		// debt, so the plan tools cannot deadlock behind an unreachable consult.
		e.consulted = true
		e.writesSinceConsult = 0
		if e.calls >= maxAdvisorCallsPerTurn {
			deny(tcc, "advisor call cap reached for this turn; proceed with the guidance you have")
			return nil
		}
		e.calls++
	case e.classify != nil && e.classify(name):
		// read-only orientation: always allowed
	case e.exempt[name]:
		// exempted by WithAdvisorExemptTools: always allowed
	case !e.consulted:
		deny(tcc, "Call `advisor` before your first state-changing action this turn. "+
			"Read-only orientation (reads, searches, listings) is allowed first. "+
			"This applies to one-line edits too.")
	case strings.HasPrefix(name, "todo"):
		// plan tools never count as writes; checkpoints need a fresh consult
		if isPlanCheckpoint(name, tcc.Call.Input) && e.writesSinceConsult > 0 {
			deny(tcc, fmt.Sprintf("Plan checkpoint: %d state-changing call(s) since your last "+
				"advisor consult. Call `advisor` before changing the plan or marking a task done.",
				e.writesSinceConsult))
		}
	default:
		e.writesSinceConsult++
	}
	return nil
}

// isPlanCheckpoint reports whether a todo tool call changes the plan's shape
// or closes a task: TodoWrite, TodoCreate, or TodoUpdate with status done.
// Unparseable TodoUpdate input is not a checkpoint; the tool rejects it anyway.
func isPlanCheckpoint(lowerName, input string) bool {
	switch lowerName {
	case "todowrite", "todocreate":
		return true
	case "todoupdate":
		var in struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(input), &in); err != nil {
			return false
		}
		return strings.EqualFold(in.Status, "done")
	}
	return false
}

func deny(tcc *core.ToolCallContext, reason string) {
	if tcc.Decision < core.Deny {
		tcc.Decision = core.Deny
		tcc.Reason = reason
	}
}
