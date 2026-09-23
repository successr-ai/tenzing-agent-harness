package systemone

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const (
	batchTools  = "tools"
	advisorTool = "advisor"
)

// OnToolBatch runs batch B over the whole issue list. Three things happen,
// in order of cost: the advisor block armed by batch A (no request), the
// working-directory rule (no request — a computed fact), then one Evaluate
// carrying two questions per pending call: is it irreversible, does it touch
// secrets.
//
// It only ever escalates, and only ever to AskUser: every one of the
// harness's three concerns is something a human should see, and none of
// them is something the model should be allowed to refuse on its own. A
// call already at Deny is not asked about. In the loop that is only the
// advisor block above: batch hooks run before every per-call hook, so
// permissions has not decided anything yet.
func (e *Ext) OnToolBatch(ctx context.Context, batch []*core.ToolCallContext) error {
	e.observe(batch)
	e.enforceAdvisorBlock(batch)
	return e.gateBatch(ctx, batch)
}

// ChildGate is the gate alone, for subagent loops. A child's calls reach the
// same filesystem, so they meet the same rule and the same two questions —
// but a child's iterations are not the main turn's: its BeforeIteration
// would reset the turn's advisor state and re-run routing, and its tool
// names would muddy batch A's state. So the child gets batch B without the
// advisor bookkeeping, and no batch A at all.
func (e *Ext) ChildGate() core.Extension { return childGate{e} }

type childGate struct{ e *Ext }

var _ core.ToolBatchHook = childGate{}

func (g childGate) Name() string { return g.e.Name() + "-child" }

func (g childGate) OnToolBatch(ctx context.Context, batch []*core.ToolCallContext) error {
	return g.e.gateBatch(ctx, batch)
}

// gateBatch is the working-directory rule plus the two questions.
func (e *Ext) gateBatch(ctx context.Context, batch []*core.ToolCallContext) error {
	if e.gate.Disabled {
		return nil
	}
	facts := make(map[string][]pathFact, len(batch))
	var decisions []core.SystemOneDecision
	judged := make([]*core.ToolCallContext, 0, len(batch))
	questions := make(map[string]common.Question, 2*len(batch))
	for _, tcc := range batch {
		if tcc.Decision == core.Deny {
			continue
		}
		facts[tcc.Call.ID] = e.pathsFor(*tcc.Call)
		decisions = append(decisions, e.enforceWorkdir(tcc, facts[tcc.Call.ID])...)
		judged = append(judged, tcc)
		questions[irreversibleID(tcc.Call.ID)] = irreversibleQuestion()
		questions[secretsID(tcc.Call.ID)] = secretsQuestion()
	}
	if len(judged) == 0 {
		return nil
	}

	started := time.Now()
	resp, ok := e.evaluate(ctx, batchTools, common.EvaluationRequest{
		State:     e.callStates(judged, facts, e.recentMessages(ctx)),
		Questions: questions,
	})
	if !ok {
		// The model is unavailable; the rule above still applied. Report it.
		if len(decisions) > 0 {
			e.report(batchTools, common.EvaluationResponse{}, started, decisions)
		}
		return nil
	}
	for _, tcc := range judged {
		decisions = append(decisions, e.applyGate(tcc, resp)...)
	}
	e.report(batchTools, resp, started, decisions)
	return nil
}

// enforceWorkdir is the gate's one rule: any path that resolves outside the
// working directory sends the call to a human, whatever the tool and however
// harmless the model might think it. A path in the system temp directory is
// exempt — scratch space is where an agent is supposed to put things it does
// not want in the project. Pure fact, no model, so it applies even when the
// endpoint is down.
func (e *Ext) enforceWorkdir(tcc *core.ToolCallContext, facts []pathFact) []core.SystemOneDecision {
	var out []core.SystemOneDecision
	for _, f := range facts {
		if !f.OutsideWorkingDirectory || f.InTempDirectory {
			continue
		}
		escalate(tcc, core.AskUser, outsideReason(f.Path))
		out = append(out, core.SystemOneDecision{
			Question: factOutsideWorkdir, Answer: f.Path, Action: "ask", Target: tcc.Call.Name,
		})
	}
	return out
}

// callStates keys each call's state by its id, so a question naming
// `tool_call` reads the one it belongs to. One state, one request, every call
// judged against it.
func (e *Ext) callStates(judged []*core.ToolCallContext, facts map[string][]pathFact, tail []string) map[string]any {
	states := make(map[string]any, len(judged))
	for _, tcc := range judged {
		states[tcc.Call.ID] = callState{
			ToolCall: toolCall{
				Name:     tcc.Call.Name,
				Origin:   tcc.Origin,
				Input:    truncate(tcc.Call.Input, maxMessageChars),
				Decision: decisionName(tcc.Decision),
				ReadOnly: e.readOnly(tcc.Call.Name),
				Paths:    facts[tcc.Call.ID],
			},
			RecentMessages: tail,
		}
	}
	return states
}

// applyGate reads one call's two answers and escalates, each against its own
// threshold. Neither can lower what an earlier hook decided.
func (e *Ext) applyGate(tcc *core.ToolCallContext, resp common.EvaluationResponse) []core.SystemOneDecision {
	var out []core.SystemOneDecision
	record := func(id string, p float64, action string) {
		out = append(out, core.SystemOneDecision{
			Question: id, Answer: formatProbability(p), Action: action, Target: tcc.Call.Name,
		})
	}
	if irrev, ok := resp.Answers[irreversibleID(tcc.Call.ID)]; ok {
		if irrev.Noul > e.gate.IrreversibleAsk {
			escalate(tcc, core.AskUser, irreversibleReason(irrev.Noul))
			record(irreversibleID(tcc.Call.ID), irrev.Noul, "ask")
		} else {
			record(irreversibleID(tcc.Call.ID), irrev.Noul, "none")
		}
	}
	if sec, ok := resp.Answers[secretsID(tcc.Call.ID)]; ok {
		if sec.Noul > e.gate.SecretsAsk {
			escalate(tcc, core.AskUser, secretsReason(sec.Noul))
			record(secretsID(tcc.Call.ID), sec.Noul, "ask")
		} else {
			record(secretsID(tcc.Call.ID), sec.Noul, "none")
		}
	}
	return out
}

// escalate raises a decision and joins the reasons, so a call flagged for
// more than one reason arrives at the approval prompt with all of them.
// Never lowers.
func escalate(tcc *core.ToolCallContext, d core.Decision, reason string) {
	if d <= tcc.Decision {
		if tcc.Decision == d && tcc.Reason != "" && !strings.Contains(tcc.Reason, reason) {
			tcc.Reason = tcc.Reason + "; " + reason
		}
		return
	}
	tcc.Decision = d
	tcc.Reason = reason
}

// observe counts consults for the next batch A state, and clears the advisor
// block the moment one is issued — matching advisor.GateExt, which also
// counts the call at issue time.
func (e *Ext) observe(batch []*core.ToolCallContext) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, tcc := range batch {
		if strings.EqualFold(tcc.Call.Name, advisorTool) {
			e.advisorArmed = false
			e.consults++
		}
	}
}

// enforceAdvisorBlock denies state-changing calls while a consult is due.
// Read-only orientation and the advisor call itself always pass — an agent
// must be able to find out where it is, and to satisfy the block.
func (e *Ext) enforceAdvisorBlock(batch []*core.ToolCallContext) {
	e.mu.Lock()
	armed := e.advisorArmed
	e.mu.Unlock()
	if !armed {
		return
	}
	for _, tcc := range batch {
		if strings.EqualFold(tcc.Call.Name, advisorTool) || e.readOnly(tcc.Call.Name) {
			continue
		}
		escalate(tcc, core.Deny, advisorBlockReason)
	}
}

// readOnly reports whether the tool mutates nothing. Unknown tools and an
// unbound classifier both count as mutating, as everywhere else in the
// harness.
func (e *Ext) readOnly(name string) bool {
	e.mu.Lock()
	classify := e.classify
	e.mu.Unlock()
	return classify != nil && classify(name)
}

func formatProbability(p float64) string { return fmt.Sprintf("%.2f", p) }
