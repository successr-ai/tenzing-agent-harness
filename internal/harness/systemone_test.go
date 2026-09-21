package harness

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// fakeJudge answers whatever is scripted for a question id, matching on the
// id's suffix so per-call gate questions ("<call id>#out_of_scope") need no
// knowledge of the model-generated call ids.
type fakeJudge struct {
	mu       sync.Mutex
	bySuffix map[string]common.Answer
	err      error
	calls    int
}

func (f *fakeJudge) Evaluate(_ context.Context, req common.EvaluationRequest) (common.EvaluationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return common.EvaluationResponse{}, f.err
	}
	answers := make(map[string]common.Answer, len(req.Questions))
	for id := range req.Questions {
		for suffix, ans := range f.bySuffix {
			if strings.HasSuffix(id, suffix) {
				answers[id] = ans
			}
		}
	}
	return common.EvaluationResponse{Model: "jev-1.13.0", Answers: answers}, nil
}

func (f *fakeJudge) GetCurrentModel() string { return "jev-fake" }

func (f *fakeJudge) seen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func yes(p float64) common.Answer {
	return common.Answer{Type: common.QuestionNoul, Noul: p}
}

func decisionEvents(t *testing.T, ch <-chan core.Event) []core.SystemOneDecisionEvent {
	t.Helper()
	var out []core.SystemOneDecisionEvent
	for {
		select {
		case ev := <-ch:
			if d, ok := ev.(core.SystemOneDecisionEvent); ok {
				out = append(out, d)
			}
		default:
			return out
		}
	}
}

// TestSystemOneGateDeniesAToolCall: the decision model judges the call out of
// scope, so it never runs and the model is told why.
func TestSystemOneGateDeniesAToolCall(t *testing.T) {
	dir := t.TempDir()
	scripted := newScriptedAgent(
		toolStep("bash", jsonInput(map[string]any{"command": "echo hi > " + dir + "/out.txt"})),
		finalStep("done"),
	)
	judge := &fakeJudge{bySuffix: map[string]common.Answer{"#out_of_scope": yes(0.99)}}
	bus := eventbus.NewEventBus()
	events := bus.Subscribe(32)

	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPermissionsDisabled(),
		WithEventBus(bus),
		WithSystemOne(systemone.Config{
			Client:  judge,
			Advisor: systemone.AdvisorConfig{Disabled: true},
		}, nil),
	)
	if _, err := h.RunTurn(context.Background(), "write it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	assertFileNotExists(t, dir+"/out.txt")

	calls := scripted.capturedCalls()
	if len(calls) < 2 {
		t.Fatalf("agent calls = %d, want 2", len(calls))
	}
	var fedBack strings.Builder
	for _, m := range calls[1].Messages {
		for _, b := range m.Content {
			fedBack.WriteString(b.ToolOutput)
		}
	}
	if !strings.Contains(fedBack.String(), "outside what you asked for") {
		t.Errorf("denial reason not fed back to the model:\n%s", fedBack.String())
	}

	evs := decisionEvents(t, events)
	if len(evs) == 0 {
		t.Fatal("the gate must emit its decision")
	}
	if evs[0].Model != "jev-1.13.0" {
		t.Errorf("event must log the version that answered, got %q", evs[0].Model)
	}
}

// TestSystemOneRoutesTheTurnsModel: routing runs inside the first iteration,
// where the between-turns guard on SetLLM cannot pass — switchLLM is the
// path, and it must actually take effect.
func TestSystemOneRoutesTheTurnsModel(t *testing.T) {
	agent := &switchableAgent{ScriptedAgent: newScriptedAgent(finalStep("done"))}
	judge := &fakeJudge{bySuffix: map[string]common.Answer{
		"model": {Type: common.QuestionChoice, Choice: "fast-model", Confidence: 0.9},
	}}
	var resolved []string
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return agent, nil }),
		WithPermissionsDisabled(),
		WithSystemOne(systemone.Config{
			Client:  judge,
			Gate:    systemone.GateConfig{Disabled: true},
			Advisor: systemone.AdvisorConfig{Disabled: true},
			Routing: systemone.RoutingConfig{
				Current: "stub-model",
				Candidates: []systemone.Candidate{
					{Name: "stub-model", Description: "the one already serving"},
					{Name: "fast-model", Description: "cheap and quick"},
				},
			},
		}, func(alias string) (common.LLM, error) {
			resolved = append(resolved, alias)
			return &namedStubLLM{name: alias}, nil
		}),
	)
	if _, err := h.RunTurn(context.Background(), "something small"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "fast-model" {
		t.Fatalf("resolver calls = %v, want one for fast-model", resolved)
	}
	if got := h.CurrentModel().GetName(); got != "fast-model" {
		t.Fatalf("CurrentModel() = %q, want the routed model", got)
	}
}

// TestSystemOneRoutingWithoutAResolver: the choice is judged but cannot be
// applied, and that must not disturb the turn.
func TestSystemOneRoutingWithoutAResolver(t *testing.T) {
	agent := &switchableAgent{ScriptedAgent: newScriptedAgent(finalStep("done"))}
	judge := &fakeJudge{bySuffix: map[string]common.Answer{
		"model": {Type: common.QuestionChoice, Choice: "fast-model", Confidence: 0.9},
	}}
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return agent, nil }),
		WithPermissionsDisabled(),
		WithSystemOne(systemone.Config{
			Client:  judge,
			Gate:    systemone.GateConfig{Disabled: true},
			Advisor: systemone.AdvisorConfig{Disabled: true},
			Routing: systemone.RoutingConfig{
				Current:    "stub-model",
				Candidates: []systemone.Candidate{{Name: "stub-model", Description: "a"}, {Name: "fast-model", Description: "b"}},
			},
		}, nil),
	)
	if _, err := h.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if got := h.CurrentModel().GetName(); got != "stub-model" {
		t.Fatalf("CurrentModel() = %q, want the configured model — nothing could apply the choice", got)
	}
}

// TestSystemOneFailsOpenAcrossAWholeTurn: an unreachable decision model
// leaves the harness doing exactly what it would have done without one.
func TestSystemOneFailsOpenAcrossAWholeTurn(t *testing.T) {
	dir := t.TempDir()
	scripted := newScriptedAgent(
		toolStep("write", jsonInput(map[string]any{"file_path": dir + "/out.txt", "content": "hi"})),
		finalStep("done"),
	)
	judge := &fakeJudge{err: errors.New("endpoint unreachable")}
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPermissionsDisabled(),
		WithSystemOne(systemone.Config{Client: judge}, nil),
	)
	tr, err := h.RunTurn(context.Background(), "write it")
	if err != nil {
		t.Fatalf("a dead decision model must not fail the turn: %v", err)
	}
	if tr != "done" {
		t.Fatalf("answer = %q, want the turn to complete", tr)
	}
	if judge.seen() == 0 {
		t.Fatal("the judge should have been tried")
	}
	if _, err := os.Stat(dir + "/out.txt"); err != nil {
		t.Fatalf("the tool must still have run: %v", err)
	}
}

// TestSystemOneUnconfiguredRunsNormally: the zero config is what every
// caller without a decision model passes, so it must register nothing and
// disturb nothing.
func TestSystemOneUnconfiguredRunsNormally(t *testing.T) {
	scripted := newScriptedAgent(finalStep("done"))
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithSystemOne(systemone.Config{}, nil),
	)
	tr, err := h.RunTurn(context.Background(), "go")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if tr != "done" {
		t.Fatalf("answer = %q", tr)
	}
}

// TestSystemOneRoutingIsRaceFreeAgainstAModelReader runs the real agent (not
// a scripted one) so the routed swap hits the actual client field, while a
// driver polls the model name the way api/controls.go does. Meaningful under
// -race; a no-op otherwise.
func TestSystemOneRoutingIsRaceFreeAgainstAModelReader(t *testing.T) {
	judge := &fakeJudge{bySuffix: map[string]common.Answer{
		"model": {Type: common.QuestionChoice, Choice: "fast-model", Confidence: 0.9},
	}}
	redirectHome(t)
	h, err := New(&namedStubLLM{name: "stub-model"},
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithSubagentDepth(0),
		WithPermissionsDisabled(),
		WithSystemOne(systemone.Config{
			Client:  judge,
			Gate:    systemone.GateConfig{Disabled: true},
			Advisor: systemone.AdvisorConfig{Disabled: true},
			Routing: systemone.RoutingConfig{
				Current:    "stub-model",
				Candidates: []systemone.Candidate{{Name: "stub-model", Description: "a"}, {Name: "fast-model", Description: "b"}},
			},
		}, func(alias string) (common.LLM, error) { return &namedStubLLM{name: alias}, nil }),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(h.Shutdown)

	stop := make(chan struct{})
	var poller sync.WaitGroup
	poller.Add(1)
	go func() {
		defer poller.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = h.GetCurrentModel()
			}
		}
	}()

	// The stub LLM answers nothing, so the turn errors out — irrelevant
	// here: the routing switch happens before the first reasoning call.
	_, _ = h.RunTurn(context.Background(), "go")
	close(stop)
	poller.Wait()

	if got := h.GetCurrentModel(); got != "fast-model" {
		t.Fatalf("GetCurrentModel() = %q, want the routed model", got)
	}
}
