package systemone

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// fakeJudge scripts one answer set and records every request it saw.
type fakeJudge struct {
	answers map[string]common.Answer
	err     error
	reqs    []common.EvaluationRequest
}

func (f *fakeJudge) Evaluate(_ context.Context, req common.EvaluationRequest) (common.EvaluationResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return common.EvaluationResponse{}, f.err
	}
	return common.EvaluationResponse{Model: "jev-1.13.0", Answers: f.answers}, nil
}

func (f *fakeJudge) GetCurrentModel() string { return "jev-fake" }

// collector captures emitted events.
type collector struct{ events []core.Event }

func (c *collector) Emit(e core.Event) { c.events = append(c.events, e) }

func noul(p float64) common.Answer {
	return common.Answer{Type: common.QuestionNoul, Noul: p}
}

func choice(name string, confidence float64) common.Answer {
	return common.Answer{Type: common.QuestionChoice, Choice: name, Confidence: confidence}
}

func call(id, name string) *core.ToolCallContext {
	return &core.ToolCallContext{Call: &core.ToolCall{ID: id, Name: name, Input: "{}"}, Origin: "native"}
}

// newGateExt builds an extension with only the gate live.
func newGateExt(j common.SystemOne) *Ext {
	return New(Config{Client: j, Advisor: AdvisorConfig{Disabled: true}})
}

func TestGateThresholds(t *testing.T) {
	tests := []struct {
		name         string
		scope, irrev float64
		start        core.Decision
		want         core.Decision
		wantReason   string
	}{
		{"both low leaves the decision alone", 0.1, 0.1, core.Allow, core.Allow, ""},
		{"scope over ask escalates to ask", 0.7, 0.0, core.Allow, core.AskUser, "outside what you asked for"},
		{"scope over deny denies", 0.95, 0.0, core.Allow, core.Deny, "outside what you asked for"},
		{"irreversible over ask escalates to ask", 0.1, 0.8, core.Allow, core.AskUser, "irreversible"},
		{"irreversible never denies on its own", 0.1, 0.99, core.Allow, core.AskUser, "irreversible"},
		{"an existing ask is not lowered", 0.0, 0.0, core.AskUser, core.AskUser, ""},
		{"ask is raised to deny", 0.95, 0.0, core.AskUser, core.Deny, "outside what you asked for"},
		{"at the threshold exactly does not fire", DefaultGateAsk, DefaultGateAsk, core.Allow, core.Allow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &fakeJudge{answers: map[string]common.Answer{
				scopeID("c1"):        noul(tt.scope),
				irreversibleID("c1"): noul(tt.irrev),
			}}
			tcc := call("c1", "write")
			tcc.Decision = tt.start
			if err := newGateExt(j).OnToolBatch(context.Background(), []*core.ToolCallContext{tcc}); err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tcc.Decision != tt.want {
				t.Fatalf("decision = %v, want %v (reason %q)", tcc.Decision, tt.want, tcc.Reason)
			}
			if tt.wantReason != "" && !strings.Contains(tcc.Reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to mention %q", tcc.Reason, tt.wantReason)
			}
		})
	}
}

func TestGateAsksTwoQuestionsPerCallInOneRequest(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{}}
	batch := []*core.ToolCallContext{call("c1", "read"), call("c2", "write")}
	if err := newGateExt(j).OnToolBatch(context.Background(), batch); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != 1 {
		t.Fatalf("one batch must be one request, got %d", len(j.reqs))
	}
	if got := len(j.reqs[0].Questions); got != 4 {
		t.Fatalf("questions = %d, want 4 (two per call)", got)
	}
}

func TestGateSkipsAlreadyDeniedCalls(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{}}
	denied := call("c1", "write")
	denied.Decision = core.Deny
	if err := newGateExt(j).OnToolBatch(context.Background(), []*core.ToolCallContext{denied}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != 0 {
		t.Fatal("a denied call cannot be escalated further; it must not be asked about")
	}
}

func TestGateDisabledAsksNothing(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{}}
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}, Advisor: AdvisorConfig{Disabled: true}})
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c1", "write")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != 0 {
		t.Fatalf("disabled gate must not call the model, got %d requests", len(j.reqs))
	}
}

func TestGateFailsOpen(t *testing.T) {
	j := &fakeJudge{err: errors.New("endpoint unreachable")}
	c := &collector{}
	ext := newGateExt(j)
	ext.SetEmitter(c)
	tcc := call("c1", "write")

	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{tcc}); err != nil {
		t.Fatalf("a failing judge must not block the batch: %v", err)
	}
	if tcc.Decision != core.Allow {
		t.Fatalf("decision = %v, want the policy's own (Allow)", tcc.Decision)
	}
	if len(c.events) != 1 {
		t.Fatalf("a failed batch must still emit, got %d events", len(c.events))
	}
	ev := c.events[0].(core.SystemOneDecisionEvent)
	if ev.Error == "" || ev.Batch != batchTools {
		t.Fatalf("event must name the failure and the batch: %+v", ev)
	}
}

func TestGateIgnoresAnswersForUnknownCalls(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{scopeID("other"): noul(0.99)}}
	tcc := call("c1", "write")
	if err := newGateExt(j).OnToolBatch(context.Background(), []*core.ToolCallContext{tcc}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tcc.Decision != core.Allow {
		t.Fatalf("an answer for another call must not touch this one, got %v", tcc.Decision)
	}
}

func TestAdvisorBlockArmsBlocksAndClears(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{qAdvisor: noul(0.9)}}
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}})
	ext.SetClassifier(func(name string) bool { return name == "read" })

	tc := &core.TurnContext{Iteration: 1}
	if err := ext.BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(tc.Reminders) != 1 {
		t.Fatalf("a due consult must remind, got %v", tc.Reminders)
	}

	write, read := call("c1", "write"), call("c2", "read")
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{write, read}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if write.Decision != core.Deny {
		t.Fatalf("armed block must deny a state-changing call, got %v", write.Decision)
	}
	if read.Decision != core.Allow {
		t.Fatal("armed block must let read-only orientation through")
	}

	consult := call("c3", "advisor")
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{consult}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if consult.Decision != core.Allow {
		t.Fatal("the block must never deny the call that satisfies it")
	}
	after := call("c4", "write")
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{after}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if after.Decision != core.Allow {
		t.Fatalf("a consult must clear the block, got %v", after.Decision)
	}
}

func TestAdvisorBelowThresholdDoesNothing(t *testing.T) {
	j := &fakeJudge{answers: map[string]common.Answer{qAdvisor: noul(DefaultAdvisorConsult)}}
	ext := New(Config{Client: j, Gate: GateConfig{Disabled: true}})
	tc := &core.TurnContext{Iteration: 1}
	if err := ext.BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(tc.Reminders) != 0 {
		t.Fatalf("at the threshold exactly must not fire, got %v", tc.Reminders)
	}
	write := call("c1", "write")
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{write}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if write.Decision != core.Allow {
		t.Fatalf("no block was armed; got %v", write.Decision)
	}
}

func TestBeforeIterationFailsOpen(t *testing.T) {
	ext := New(Config{Client: &fakeJudge{err: errors.New("down")}, Gate: GateConfig{Disabled: true}})
	tc := &core.TurnContext{Iteration: 1}
	if err := ext.BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("a failing judge must never block an iteration: %v", err)
	}
	if len(tc.Reminders) != 0 {
		t.Fatal("a failed batch must change nothing")
	}
}

func TestNoQuestionsMeansNoRequest(t *testing.T) {
	j := &fakeJudge{}
	ext := New(Config{Client: j, Advisor: AdvisorConfig{Disabled: true}}) // routing has no candidates
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != 0 {
		t.Fatalf("nothing to ask means no request, got %d", len(j.reqs))
	}
}

func TestNewWithoutClientIsNil(t *testing.T) {
	if ext := New(Config{}); ext != nil {
		t.Fatal("no client means no extension")
	}
}

// slowJudge blocks until its context is done, standing in for the retry
// ladder the protocol client runs against an unreachable endpoint.
type slowJudge struct {
	mu    sync.Mutex
	calls int
}

func (s *slowJudge) Evaluate(ctx context.Context, _ common.EvaluationRequest) (common.EvaluationResponse, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	<-ctx.Done()
	return common.EvaluationResponse{}, ctx.Err()
}

func (s *slowJudge) GetCurrentModel() string { return "slow" }

func (s *slowJudge) seen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// A batch must not outlive its deadline: fail-open is only useful if it is
// also fail-fast.
func TestBatchTimeoutBoundsAFailingJudge(t *testing.T) {
	j := &slowJudge{}
	ext := New(Config{Client: j, Advisor: AdvisorConfig{Disabled: true}, Timeout: 20 * time.Millisecond})
	tcc := call("c1", "write")

	start := time.Now()
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{tcc}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("batch took %s; the deadline must cancel it", elapsed)
	}
	if tcc.Decision != core.Allow {
		t.Fatalf("a timed-out batch must change nothing, got %v", tcc.Decision)
	}
}

// After enough consecutive failures the extension stops calling for the rest
// of the turn, and the next turn re-arms it.
func TestFailureBreakerStopsCallingAndReArmsNextTurn(t *testing.T) {
	j := &fakeJudge{err: errors.New("unreachable")}
	ext := New(Config{Client: j, Advisor: AdvisorConfig{Disabled: true}})

	for i := 0; i < maxConsecutiveFailures+3; i++ {
		if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")}); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if len(j.reqs) != maxConsecutiveFailures {
		t.Fatalf("attempts = %d, want the breaker to stop at %d", len(j.reqs), maxConsecutiveFailures)
	}

	// A new turn tries again.
	if err := ext.BeforeIteration(context.Background(), &core.TurnContext{Iteration: 1}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(j.reqs) != maxConsecutiveFailures+1 {
		t.Fatalf("attempts = %d, want one more after the turn boundary", len(j.reqs))
	}
}

// A success clears the count, so intermittent blips never trip the breaker.
func TestBreakerResetsOnSuccess(t *testing.T) {
	j := &fakeJudge{err: errors.New("blip")}
	ext := New(Config{Client: j, Advisor: AdvisorConfig{Disabled: true}})
	for i := 0; i < maxConsecutiveFailures-1; i++ {
		_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")})
	}
	j.err = nil
	j.answers = map[string]common.Answer{}
	_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")})

	j.err = errors.New("blip")
	for i := 0; i < maxConsecutiveFailures-1; i++ {
		_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")})
	}
	before := len(j.reqs)
	_ = ext.OnToolBatch(context.Background(), []*core.ToolCallContext{call("c", "write")})
	if len(j.reqs) != before+1 {
		t.Fatal("a success must clear the failure count")
	}
}
