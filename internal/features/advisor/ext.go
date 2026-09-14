package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Gate defaults, used when GateConfig leaves the field zero.
const (
	// DefaultMaxCallsPerTurn bounds consults per turn so a looping executor
	// cannot burn advisor tokens indefinitely. Sized for the plan-checkpoint
	// rule plus a cadence consult every DefaultCadence iterations on a long
	// turn.
	DefaultMaxCallsPerTurn = 30

	// DefaultCadence is the number of loop iterations the executor may run
	// without consulting before the gate blocks every tool until it does.
	// Catches an executor oscillating between hypotheses on its own — the
	// gate is otherwise silent after the turn's first consult.
	DefaultCadence = 6
)

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
	"If you reverse a decision you made earlier this turn (\"actually\", \"wait\", " +
	"\"I had it right the first time\"), stop reasoning: your next call is `advisor`, " +
	"with both hypotheses in the `question`. Do not test a third variant first.\n\n" +
	"Hard rules (enforced):\n" +
	"- Your first state-changing tool call each turn must be preceded by an advisor call. " +
	"Read-only orientation is always allowed first. This applies to one-line edits too.\n" +
	"- Plan checkpoints: `TodoWrite`, `TodoCreate`, and `TodoUpdate` with status `done` " +
	"require a fresh advisor call — one with no state-changing tool calls since. " +
	"Consult, then write or change the plan; finish a task, consult, then mark it done."

// cadenceRule is appended to gatePromptFragment when the cadence rule is on.
const cadenceRule = "\n- Cadence: after %d loop iterations without a consult, every tool is " +
	"blocked until you call `advisor`."

const nudgeReminder = "You have not consulted the advisor yet. If the task has a " +
	"non-obvious design decision or a failure mode you haven't ruled out, call " +
	"advisor now before committing to an approach."

const cadenceReminder = "You have gone several iterations without consulting the advisor. " +
	"Your next tool call must be `advisor`. State what you believe, what contradicts it, " +
	"and the decision you keep reversing."

var (
	_ core.Extension           = (*GateExt)(nil)
	_ core.ToolCallHook        = (*GateExt)(nil)
	_ core.BeforeIterationHook = (*GateExt)(nil)
	_ core.PromptContributor   = (*GateExt)(nil)
)

// GateConfig configures GateExt. Zero values mean: no nudge, DefaultCadence,
// DefaultMaxCallsPerTurn, no exempt tools. Cadence < 0 disables the cadence
// rule.
type GateConfig struct {
	// NudgeIteration, when > 0, appends a reminder from that iteration
	// onward on turns where the advisor has not been consulted (off by
	// default — Anthropic measured nudging counterproductive on strong
	// executor models).
	NudgeIteration int
	// Cadence is the max loop iterations between consults before every
	// tool is blocked. 0 = DefaultCadence; < 0 disables.
	Cadence int
	// MaxCallsPerTurn caps consults per turn. 0 = DefaultMaxCallsPerTurn.
	MaxCallsPerTurn int
	// ExemptTools names tools that bypass the gate even unconsulted (e.g. a
	// harness's forced single-shot answer tool, which has no orientation
	// phase to precede it).
	ExemptTools []string
}

// GateExt enforces the advisor "hard rules" on the main loop. Per turn, the
// first state-changing tool call is denied until the advisor has been called.
// Plan checkpoints (TodoWrite, TodoCreate, TodoUpdate to done) are denied
// unless the advisor has been called since the last state-changing tool call,
// so the advisor reviews at every plan change and task boundary. Todo tools
// themselves never count as state-changing for that rule, so status flips
// between checkpoints flow freely. After Cadence iterations without a
// consult every tool (read-only included) is denied until the advisor is
// called, so an executor cannot oscillate indefinitely on its own; the rule
// yields once the per-turn call cap is reached so it cannot deadlock.
// Read-only tools otherwise flow freely; unknown tools (no read-only marker,
// e.g. MCP) count as state-changing. Exempt tools flow freely, unconsulted.
// classify is late-bound like readOnlyExt's: the composite ToolPort is built
// after the extension set, so harness.New assigns it via SetClassifier before
// any turn runs.
type GateExt struct {
	nudgeIteration int
	cadence        int
	maxCalls       int
	exempt         map[string]bool

	mu                 sync.Mutex
	classify           func(name string) bool // true = read-only
	consulted          bool
	calls              int
	writesSinceConsult int
	itersSinceConsult  int
}

// NewGateExt builds the write-gate from cfg (see GateConfig for zero-value
// semantics).
func NewGateExt(cfg GateConfig) *GateExt {
	var exempt map[string]bool
	if len(cfg.ExemptTools) > 0 {
		exempt = make(map[string]bool, len(cfg.ExemptTools))
		for _, name := range cfg.ExemptTools {
			exempt[strings.ToLower(name)] = true
		}
	}
	cadence := cfg.Cadence
	if cadence == 0 {
		cadence = DefaultCadence
	}
	maxCalls := cfg.MaxCallsPerTurn
	if maxCalls <= 0 {
		maxCalls = DefaultMaxCallsPerTurn
	}
	return &GateExt{
		nudgeIteration: cfg.NudgeIteration,
		cadence:        cadence,
		maxCalls:       maxCalls,
		exempt:         exempt,
	}
}

// SetClassifier late-binds the read-only classifier (composite.ReadOnly).
func (e *GateExt) SetClassifier(classify func(name string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.classify = classify
}

func (e *GateExt) Name() string { return "advisor-gate" }

func (e *GateExt) PromptFragment() string {
	if e.cadence <= 0 {
		return gatePromptFragment
	}
	return gatePromptFragment + fmt.Sprintf(cadenceRule, e.cadence)
}

// BeforeIteration resets the per-turn state on the first iteration, counts
// iterations since the last consult, and appends the nudge/cadence reminders
// when due.
func (e *GateExt) BeforeIteration(_ context.Context, tc *core.TurnContext) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tc.Iteration <= 1 {
		e.consulted = false
		e.calls = 0
		e.writesSinceConsult = 0
		e.itersSinceConsult = 0
	} else {
		e.itersSinceConsult++
	}
	if e.nudgeIteration > 0 && tc.Iteration >= e.nudgeIteration && !e.consulted {
		tc.Reminders = append(tc.Reminders, nudgeReminder)
	}
	if e.cadenceDue() {
		tc.Reminders = append(tc.Reminders, cadenceReminder)
	}
	return nil
}

// cadenceDue reports whether the cadence rule should block: enabled, enough
// iterations elapsed, and the call cap not yet reached (a capped executor
// could never satisfy the rule). Caller holds e.mu.
func (e *GateExt) cadenceDue() bool {
	return e.cadence > 0 && e.itersSinceConsult >= e.cadence && e.calls < e.maxCalls
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
		e.itersSinceConsult = 0
		if e.calls >= e.maxCalls {
			deny(tcc, "advisor call cap reached for this turn; proceed with the guidance you have")
			return nil
		}
		e.calls++
	case e.exempt[name]:
		// exempted by WithAdvisorExemptTools: always allowed
	case e.cadenceDue():
		deny(tcc, fmt.Sprintf("%d iterations since your last advisor consult. Stop and call "+
			"`advisor` now. Put your competing hypotheses in the `question`.", e.itersSinceConsult))
	case e.classify != nil && e.classify(name):
		// read-only orientation: always allowed
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
