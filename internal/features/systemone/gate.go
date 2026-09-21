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

// OnToolBatch runs batch B over the whole issue list: two atomic questions
// per pending call in one request. It also enforces the advisor block armed
// by batch A, which costs no request at all.
//
// It only ever escalates. A call already denied is left alone and not asked
// about — the answer could not change anything, and it would pay state for
// nothing.
func (e *Ext) OnToolBatch(ctx context.Context, batch []*core.ToolCallContext) error {
	e.observe(batch)
	e.enforceAdvisorBlock(batch)

	if e.gate.Disabled {
		return nil
	}
	judged := make([]*core.ToolCallContext, 0, len(batch))
	questions := make(map[string]common.Question, 2*len(batch))
	tail := e.recentMessages(ctx)
	for _, tcc := range batch {
		if tcc.Decision == core.Deny {
			continue
		}
		judged = append(judged, tcc)
		questions[scopeID(tcc.Call.ID)] = scopeQuestion()
		questions[irreversibleID(tcc.Call.ID)] = irreversibleQuestion()
	}
	if len(judged) == 0 {
		return nil
	}

	started := time.Now()
	resp, ok := e.evaluate(ctx, batchTools, common.EvaluationRequest{
		State:     e.callStates(judged, tail),
		Questions: questions,
	})
	if !ok {
		return nil
	}

	decisions := make([]core.SystemOneDecision, 0, len(judged))
	for _, tcc := range judged {
		decisions = append(decisions, e.applyGate(tcc, resp)...)
	}
	e.report(batchTools, resp, started, decisions)
	return nil
}

// callStates keys each call's state by its id, so a question naming
// `tool_call` reads the one it belongs to. One state, one request, every call
// judged against it.
func (e *Ext) callStates(judged []*core.ToolCallContext, tail []string) map[string]any {
	states := make(map[string]any, len(judged))
	for _, tcc := range judged {
		states[tcc.Call.ID] = callState{
			ToolCall: toolCall{
				Name:     tcc.Call.Name,
				Origin:   tcc.Origin,
				Input:    truncate(tcc.Call.Input, maxMessageChars),
				Decision: decisionName(tcc.Decision),
				ReadOnly: e.readOnly(tcc.Call.Name),
			},
			RecentMessages: tail,
		}
	}
	return states
}

// applyGate reads one call's two answers and escalates. Deny wins over ask;
// neither can lower what the policy already decided.
func (e *Ext) applyGate(tcc *core.ToolCallContext, resp common.EvaluationResponse) []core.SystemOneDecision {
	scope, hasScope := resp.Answers[scopeID(tcc.Call.ID)]
	irrev, hasIrrev := resp.Answers[irreversibleID(tcc.Call.ID)]

	var out []core.SystemOneDecision
	record := func(id string, p float64, action string) {
		out = append(out, core.SystemOneDecision{
			Question: id, Answer: formatProbability(p), Action: action, Target: tcc.Call.Name,
		})
	}

	switch {
	case hasScope && scope.Noul > e.gate.DenyAbove:
		escalate(tcc, core.Deny, scopeReason(scope.Noul, true))
		record(scopeID(tcc.Call.ID), scope.Noul, "deny")
	case hasScope && scope.Noul > e.gate.AskAbove:
		escalate(tcc, core.AskUser, scopeReason(scope.Noul, false))
		record(scopeID(tcc.Call.ID), scope.Noul, "ask")
	case hasScope:
		record(scopeID(tcc.Call.ID), scope.Noul, "none")
	}

	switch {
	case hasIrrev && irrev.Noul > e.gate.AskAbove:
		escalate(tcc, core.AskUser, irreversibleReason(irrev.Noul))
		record(irreversibleID(tcc.Call.ID), irrev.Noul, "ask")
	case hasIrrev:
		record(irreversibleID(tcc.Call.ID), irrev.Noul, "none")
	}
	return out
}

// escalate raises a decision and joins the reasons, so a call flagged by both
// questions arrives at the approval prompt with both. Never lowers.
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

// observe records the turn's tool names for the next batch A state, and
// clears the advisor block the moment a consult is issued — matching
// advisor.GateExt, which also counts the call at issue time.
func (e *Ext) observe(batch []*core.ToolCallContext) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, tcc := range batch {
		e.recentTools = append(e.recentTools, tcc.Call.Name)
		if strings.EqualFold(tcc.Call.Name, advisorTool) {
			e.advisorArmed = false
			e.consults++
		}
	}
	if n := len(e.recentTools); n > maxRecentTools {
		e.recentTools = append([]string(nil), e.recentTools[n-maxRecentTools:]...)
	}
}

// maxRecentTools bounds the tool log carried in batch A's state.
const maxRecentTools = 12

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
