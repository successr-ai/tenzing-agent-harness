package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// fakeJudge answers whatever is scripted for a question id, matching on the
// id's suffix so per-call gate questions ("<call id>#irreversible") need no
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

// outsideTarget is a file just outside the harness's working directory (the
// package dir) and outside the temp root, which the gate exempts. Removed on
// cleanup in case a regression lets the write through.
func outsideTarget(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(wd), fmt.Sprintf("zz-gate-%s.txt", t.Name()))
	t.Cleanup(func() { _ = os.Remove(target) })
	return target
}

// TestSystemOneGateStopsAWriteOutsideTheWorkingDirectory: with a decision
// model configured, a write outside the project reaches a human — who
// declines, so it never lands. The judge answers nothing: the rule is a fact
// and holds without it.
func TestSystemOneGateStopsAWriteOutsideTheWorkingDirectory(t *testing.T) {
	target := outsideTarget(t)
	scripted := newScriptedAgent(
		toolStep("Write", jsonInput(map[string]any{"file_path": target, "content": "hi"})),
		finalStep("done"),
	)
	judge := &fakeJudge{}
	bus := eventbus.NewEventBus()
	events := bus.Subscribe(32)
	var reasons []string

	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPermissionsDisabled(),
		WithEventBus(bus),
		WithHooks(eventbus.Hooks{OnApprovalRequested: func(e core.ApprovalRequestedEvent) {
			reasons = append(reasons, e.Reason)
			e.Respond(false)
		}}),
		WithSystemOne(systemone.Config{
			Client:  judge,
			Advisor: systemone.AdvisorConfig{Disabled: true},
		}, nil),
	)
	if _, err := h.RunTurn(context.Background(), "write it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	assertFileNotExists(t, target)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "outside the working directory") {
		t.Fatalf("approval reasons = %q, want one naming the working directory", reasons)
	}

	evs := decisionEvents(t, events)
	if len(evs) == 0 {
		t.Fatal("the gate must emit its decision")
	}
	if evs[0].Model != "jev-1.13.0" {
		t.Errorf("event must log the version that answered, got %q", evs[0].Model)
	}
}

// TestSystemOneGateCoversSubagents: a child loop touches the same
// filesystem, so delegating a write must not route around the gate. The
// child has no approver, so the escalation denies outright.
func TestSystemOneGateCoversSubagents(t *testing.T) {
	target := outsideTarget(t)
	main := newScriptedAgent(
		toolStep("spawn_agent", jsonInput(map[string]any{"task": "write the file"})),
		finalStep("done"),
	)
	child := newScriptedAgent(
		toolStep("Write", jsonInput(map[string]any{"file_path": target, "content": "hi"})),
		finalStep("child-done"),
	)
	builds := 0
	builder := func(common.LLM, string) (core.Agent, error) {
		builds++
		if builds == 1 {
			return main, nil
		}
		return child, nil
	}
	h := newTestHarness(t,
		WithAgentBuilder(builder),
		WithPermissionsDisabled(),
		WithSystemOne(systemone.Config{
			Client:  &fakeJudge{},
			Advisor: systemone.AdvisorConfig{Disabled: true},
		}, nil),
	)
	if _, err := h.RunTurn(context.Background(), "delegate it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if child.callCount() == 0 {
		t.Fatal("the child never ran; the test proves nothing")
	}
	assertFileNotExists(t, target)
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

// TestShellWordsIncludeRedirectTargets: the gate finds paths in the words
// shellWords returns, so a redirect target must be one of them — `echo x >
// ~/.zshrc` names no path in its argv.
func TestShellWordsIncludeRedirectTargets(t *testing.T) {
	got := strings.Join(shellWords("echo x >> ~/.zshrc && sort < /etc/hosts"), " ")
	if want := "echo x ~/.zshrc sort /etc/hosts"; got != want {
		t.Fatalf("shellWords = %q, want %q", got, want)
	}
}

// TestShellWordsExpandPlainParameters: `$HOME/.ssh` is where the bash tool
// would look, since it inherits this environment; an unset variable drops
// the word rather than inventing a location.
func TestShellWordsExpandPlainParameters(t *testing.T) {
	t.Setenv("ZZ_GATE_DIR", "/etc")
	got := strings.Join(shellWords(`cat "$ZZ_GATE_DIR/hosts" $ZZ_UNSET_VAR/x > ${ZZ_GATE_DIR}/out`), " ")
	if want := "cat /etc/hosts /etc/out"; got != want {
		t.Fatalf("shellWords = %q, want %q", got, want)
	}
}

// adviceJudge answers needs_advisor from a script, one entry per batch A
// (one per iteration), and nothing else.
type adviceJudge struct {
	mu    sync.Mutex
	needs []float64
}

func (j *adviceJudge) Evaluate(_ context.Context, req common.EvaluationRequest) (common.EvaluationResponse, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	answers := map[string]common.Answer{}
	if _, ok := req.Questions["needs_advisor"]; ok && len(j.needs) > 0 {
		answers["needs_advisor"] = yes(j.needs[0])
		j.needs = j.needs[1:]
	}
	return common.EvaluationResponse{Model: "jev-1.13.0", Answers: answers}, nil
}

func (j *adviceJudge) GetCurrentModel() string { return "jev-fake" }

// TestSystemOneSendsTheExecutorToItsAdvisor: after the turn's first consult
// (which the advisor gate already requires), the decision model judges a
// second one due. The executor is reminded, its next write is refused until
// it calls advisor, and the same write then runs.
func TestSystemOneSendsTheExecutorToItsAdvisor(t *testing.T) {
	dir := t.TempDir()
	blocked, allowed := filepath.Join(dir, "blocked.txt"), filepath.Join(dir, "allowed.txt")
	scripted := newScriptedAgent(
		toolStep("advisor", jsonInput(map[string]any{"question": "plan?"})),
		toolStep("Write", jsonInput(map[string]any{"file_path": blocked, "content": "x"})),
		toolStep("advisor", jsonInput(map[string]any{"question": "still right?"})),
		toolStep("Write", jsonInput(map[string]any{"file_path": allowed, "content": "x"})),
		finalStep("done"),
	)
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPermissionsDisabled(),
		WithAdvisorLLM(&stubLLM{}),
		WithSystemOne(systemone.Config{
			Client: &adviceJudge{needs: []float64{0.1, 0.95, 0.1, 0.1, 0.1}},
			Gate:   systemone.GateConfig{Disabled: true},
		}, nil),
	)
	if _, err := h.RunTurn(context.Background(), "change it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	calls := scripted.capturedCalls()
	if len(calls) != 5 {
		t.Fatalf("agent calls = %d, want 5", len(calls))
	}
	if !strings.Contains(strings.Join(calls[1].Reminders, "\n"), "Your advisor should see this") {
		t.Errorf("iteration 2 reminders = %q, want the consult reminder", calls[1].Reminders)
	}
	assertFileNotExists(t, blocked)
	if !strings.Contains(lastToolOutput(calls[2]), "Call `advisor` before this") {
		t.Errorf("blocked write's result = %q, want the advisor block", lastToolOutput(calls[2]))
	}
	if _, err := os.Stat(allowed); err != nil {
		t.Fatalf("the write after the consult must run: %v", err)
	}
}

func lastToolOutput(c capturedCall) string {
	var b strings.Builder
	if n := len(c.Messages); n > 0 {
		for _, blk := range c.Messages[n-1].Content {
			b.WriteString(blk.ToolOutput)
		}
	}
	return b.String()
}
