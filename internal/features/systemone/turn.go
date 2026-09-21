package systemone

import (
	"context"
	"log/slog"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const batchTurn = "turn"

// BeforeIteration runs batch A: routing (first iteration only) and the
// advisor-need judgment (every iteration). It never returns an error —
// BeforeIteration is load-bearing, and a decision model that cannot answer
// must not be able to stall the turn.
func (e *Ext) BeforeIteration(ctx context.Context, tc *core.TurnContext) error {
	first := tc.Iteration <= 1
	e.mu.Lock()
	e.runnerID = tc.RunnerID
	if first {
		e.advisorArmed = false
		e.recentTools = nil
		e.consults = 0
		e.failures = 0 // a new turn re-arms the failure breaker
	}
	e.mu.Unlock()

	questions := map[string]common.Question{}
	if !e.advisor.Disabled {
		questions[qAdvisor] = advisorQuestion()
	}
	if first && e.routingEnabled() {
		questions[qRouting] = routingQuestion(e.routing.Candidates)
	}
	if len(questions) == 0 {
		return nil
	}

	started := time.Now()
	resp, ok := e.evaluate(ctx, batchTurn, common.EvaluationRequest{
		State:     e.turnState(ctx, tc),
		Questions: questions,
	})
	if !ok {
		return nil
	}

	var decisions []core.SystemOneDecision
	if d, ok := e.applyAdvisor(resp, tc); ok {
		decisions = append(decisions, d)
	}
	if d, ok := e.applyRouting(ctx, resp); ok {
		decisions = append(decisions, d)
	}
	e.report(batchTurn, resp, started, decisions)
	return nil
}

func (e *Ext) turnState(ctx context.Context, tc *core.TurnContext) turnState {
	e.mu.Lock()
	tools := append([]string(nil), e.recentTools...)
	consults := e.consults
	e.mu.Unlock()
	return turnState{
		Iteration:       tc.Iteration,
		ElapsedSeconds:  int(tc.Elapsed.Seconds()),
		RecentTools:     tools,
		AdvisorConsults: consults,
		RecentMessages:  e.recentMessages(ctx),
	}
}

// applyAdvisor arms the block and appends the reminder when a consult is due.
// Arming is sticky until the executor actually calls advisor, so an agent that
// ignores the reminder meets the block on its next state-changing call.
func (e *Ext) applyAdvisor(resp common.EvaluationResponse, tc *core.TurnContext) (core.SystemOneDecision, bool) {
	ans, ok := resp.Answers[qAdvisor]
	if !ok {
		return core.SystemOneDecision{}, false
	}
	d := core.SystemOneDecision{Question: qAdvisor, Answer: formatProbability(ans.Noul), Action: "none"}
	if ans.Noul > e.advisor.ConsultAbove {
		e.mu.Lock()
		e.advisorArmed = true
		e.mu.Unlock()
		tc.Reminders = append(tc.Reminders, advisorReminder)
		d.Action = "remind"
		d.Target = "advisor"
	}
	return d, true
}

// applyRouting switches the main model when the choice is a different
// candidate and the answer is confident enough. A low-confidence answer is no
// answer: the configured model keeps serving.
func (e *Ext) applyRouting(ctx context.Context, resp common.EvaluationResponse) (core.SystemOneDecision, bool) {
	ans, ok := resp.Answers[qRouting]
	if !ok {
		return core.SystemOneDecision{}, false
	}
	d := core.SystemOneDecision{
		Question:   qRouting,
		Answer:     ans.Choice,
		Confidence: ans.Confidence,
		Action:     "none",
		Target:     ans.Choice,
	}
	e.mu.Lock()
	route, current := e.router, e.routing.Current
	e.mu.Unlock()

	switch {
	case ans.Confidence < e.routing.MinConfidence,
		ans.Choice == "", ans.Choice == current,
		!e.isCandidate(ans.Choice), route == nil:
		return d, true
	}
	if err := route(ctx, ans.Choice); err != nil {
		slog.Warn("system one: routing choice not applied; keeping current model",
			"ext", e.Name(), "model", ans.Choice, "error", err)
		return d, true
	}
	e.mu.Lock()
	e.routing.Current = ans.Choice
	e.mu.Unlock()
	d.Action = "route"
	return d, true
}

// isCandidate guards against an answer naming something that is not on the
// menu — the endpoint should not, but an unrecognized alias must never reach
// the model registry as a lookup.
func (e *Ext) isCandidate(name string) bool {
	for _, c := range e.routing.Candidates {
		if c.Name == name {
			return true
		}
	}
	return false
}
