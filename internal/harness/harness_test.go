package harness

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/prompts"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type stubAgent struct{}

func (s *stubAgent) GetCurrentModel() string               { return "stub-model" }
func (s *stubAgent) UpdateStreamCallback(_ func(string))   {}
func (s *stubAgent) UpdateThinkingCallback(_ func(string)) {}

func (s *stubAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	return core.ReasoningResult{
		FinalAnswer: "done",
		Meta: core.ResponseMeta{
			AssistantText:    "done",
			AssistantMessage: common.NewAssistantMessage("done"),
		},
	}, nil
}

type stubLLM struct{ model common.Model }

func (s *stubLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, _ chan<- common.StreamEvent) error {
	return nil
}
func (s *stubLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (s *stubLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) {
	return nil, nil
}
func (s *stubLLM) GetCurrentModel() string   { return "stub" }
func (s *stubLLM) GetContextWindowSize() int { return 4096 }

func (s *stubLLM) GetModel() common.Model {
	if s.model != nil {
		return s.model
	}
	return testModel
}

var testModel = common.ModelDefinition{Name: "stub-model", Provider: "ollama"}

func stubBrain(_ common.LLM, _ string) (core.Agent, error) { return &stubAgent{}, nil }

// newTestHarness builds a harness with stubbed LLMs and brain. HOME is
// redirected so the memory sweep and persistence never touch real dirs.
func newTestHarness(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	redirectHome(t)
	h, err := New(&stubLLM{}, append([]HarnessOption{
		WithAgentBuilder(stubBrain),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
	}, opts...)...)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	// Stop async persisters (session/memory hooks) before the redirected
	// HOME TempDir is removed. Shutdown is idempotent, so tests may also
	// call it themselves.
	t.Cleanup(h.Shutdown)
	return h
}

func TestHarnessCreatesRunner(t *testing.T) {
	newTestHarness(t)
}

// Regression: without WithSystemPrompt, the runner logged the default prompt
// but the agent was built with "" — every main-agent request went to the LLM
// with an empty system prompt.
func TestMainAgentBuiltWithResolvedSystemPrompt(t *testing.T) {
	tests := []struct {
		name string
		opts []HarnessOption
		want string
	}{
		{"explicit prompt", []HarnessOption{WithSystemPrompt("custom prompt")}, "custom prompt"},
		{"default prompt when unset", nil, prompts.DefaultSystemPrompt()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redirectHome(t)
			var captured string
			builder := func(_ common.LLM, sp string) (core.Agent, error) {
				captured = sp
				return &stubAgent{}, nil
			}
			h, err := New(&stubLLM{}, append([]HarnessOption{
				WithAgentBuilder(builder),
				WithContextFilesDisabled(),
			}, tt.opts...)...)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			if captured == "" {
				t.Fatal("agent built with empty system prompt")
			}
			if captured != tt.want {
				t.Errorf("agent system prompt = %q, want %q", captured, tt.want)
			}
			// The runner's copy (logging/accessor) must match what the agent got.
			if h.SystemPrompt() != captured {
				t.Errorf("runner prompt %q != agent prompt %q", h.SystemPrompt(), captured)
			}
		})
	}
}

func TestHarnessRegistersSpawnAgentByDefault(t *testing.T) {
	h := newTestHarness(t)
	found := false
	for _, def := range h.ToolDefinitions() {
		if def.Name == "spawn_agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("spawn_agent tool not registered by default (depth 1)")
	}
}

func TestHarnessNoSpawnAgentWhenDepthZero(t *testing.T) {
	h := newTestHarness(t, WithSubagentDepth(0))
	for _, def := range h.ToolDefinitions() {
		if def.Name == "spawn_agent" {
			t.Fatal("spawn_agent tool should not be registered when depth is 0")
		}
	}
}

func TestHarnessAdvisorRegistration(t *testing.T) {
	tests := []struct {
		name string
		opts []HarnessOption
		want bool
	}{
		{"advisor model set", []HarnessOption{WithAdvisorLLM(&stubLLM{})}, true},
		{"no advisor model", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness(t, tt.opts...)
			found := false
			for _, def := range h.ToolDefinitions() {
				if def.Name == "advisor" {
					found = true
					break
				}
			}
			if found != tt.want {
				t.Errorf("advisor registered = %v, want %v", found, tt.want)
			}
			// The write-gate's prompt fragment rides with the tool: present
			// exactly when the advisor is enabled.
			hasMandate := strings.Contains(h.SystemPrompt(), "state-changing")
			if hasMandate != tt.want {
				t.Errorf("advisor gate mandate in system prompt = %v, want %v", hasMandate, tt.want)
			}
		})
	}
}

// recordingLLM captures SendSyncMessage requests (the advisor's consults)
// and answers with canned advice.
type recordingLLM struct {
	stubLLM
	mu   sync.Mutex
	reqs []common.CompletionRequest
}

func (r *recordingLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return common.CompletionResponse{
		Content: []common.ContentBlock{common.NewTextContent("canned advice")},
	}, nil
}

func (r *recordingLLM) requests() []common.CompletionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]common.CompletionRequest(nil), r.reqs...)
}

// With an advisor configured, the write-gate denies the turn's first
// state-changing call until the advisor has been consulted; the advisor's
// request carries the live conversation transcript.
func TestHarnessAdvisorWriteGate(t *testing.T) {
	redirectHome(t)
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")

	agent := newScriptedAgent(
		toolStep("write", jsonInput(map[string]any{"file_path": outFile, "content": "x"})),
		toolStep("advisor", `{}`),
		toolStep("write", jsonInput(map[string]any{"file_path": outFile, "content": "x"})),
		finalStep("done"),
	)
	advisorLLM := &recordingLLM{}

	h, err := New(&stubLLM{},
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithPermissionsDisabled(), // isolate the gate from approval prompts
		WithAdvisorLLM(advisorLLM),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	answer, err := h.RunTurn(context.Background(), "please write the file")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if answer != "done" {
		t.Errorf("answer = %q", answer)
	}

	calls := agent.capturedCalls()
	if len(calls) != 4 {
		t.Fatalf("agent calls = %d, want 4", len(calls))
	}
	lastOutput := func(c capturedCall) string {
		msg := c.Messages[len(c.Messages)-1]
		var out strings.Builder
		for _, block := range msg.Content {
			out.WriteString(block.ToolOutput)
		}
		return out.String()
	}
	if got := lastOutput(calls[1]); !strings.Contains(got, "advisor") {
		t.Errorf("first write result = %q, want gate denial mentioning advisor", got)
	}
	if got := lastOutput(calls[2]); got != "canned advice" {
		t.Errorf("advisor result = %q, want canned advice", got)
	}
	if got := lastOutput(calls[3]); strings.Contains(got, "advisor") {
		t.Errorf("post-consult write result = %q, want execution, not denial", got)
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Errorf("post-consult write did not create file: %v", err)
	}

	// The advisor saw the live transcript, not a model-authored summary.
	reqs := advisorLLM.requests()
	if len(reqs) != 1 {
		t.Fatalf("advisor consults = %d, want 1", len(reqs))
	}
	body := reqs[0].Messages[0].Content[0].Text
	if !strings.Contains(body, "please write the file") {
		t.Errorf("advisor transcript missing user turn: %q", body)
	}
}

// WithAdvisorExemptTools lets a named tool through as a turn's first call
// even unconsulted, and does not itself count as consulting the advisor.
func TestHarnessAdvisorExemptTools(t *testing.T) {
	redirectHome(t)
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")

	agent := newScriptedAgent(
		toolStep("write", jsonInput(map[string]any{"file_path": outFile, "content": "x"})),
		finalStep("done"),
	)
	advisorLLM := &recordingLLM{}

	h, err := New(&stubLLM{},
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithPermissionsDisabled(),
		WithAdvisorLLM(advisorLLM),
		WithAdvisorExemptTools("write"),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	answer, err := h.RunTurn(context.Background(), "please write the file")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if answer != "done" {
		t.Errorf("answer = %q", answer)
	}

	calls := agent.capturedCalls()
	if len(calls) != 2 {
		t.Fatalf("agent calls = %d, want 2 (exempted write should not be denied)", len(calls))
	}
	lastOutput := func(c capturedCall) string {
		msg := c.Messages[len(c.Messages)-1]
		var out strings.Builder
		for _, block := range msg.Content {
			out.WriteString(block.ToolOutput)
		}
		return out.String()
	}
	if got := lastOutput(calls[1]); strings.Contains(got, "advisor") {
		t.Errorf("exempted write result = %q, want execution, not gate denial", got)
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Errorf("exempted write did not create file: %v", err)
	}
	if reqs := advisorLLM.requests(); len(reqs) != 0 {
		t.Errorf("advisor consults = %d, want 0 (exempted tool never triggers a consult)", len(reqs))
	}
}

// TestHarnessAdvisorGate_SingleShotForcedToolIncident reproduces the
// starscream-document-api incident: a closed, single-shot harness whose only
// action is one forced schema tool call (grid proposal / classification) was
// wired with WithAdvisorLLM. The gate has no orientation phase to hide
// behind, so it denies that forced call, and the executor needs
// deny→advisor→retry — 3 round-trips where the harness budget was sized for
// 1. The budget here (MaxIterations: 2) is exactly what an unaugmented
// "call the tool, then answer" shape needs; it's what proposeGridBudget
// represented before the advisor was wired in without widening it.
func TestHarnessAdvisorGate_SingleShotForcedToolIncident(t *testing.T) {
	redirectHome(t)

	t.Run("ungated advisor exhausts the unwidened budget", func(t *testing.T) {
		agent := newScriptedAgent(
			toolStep("propose_grid", jsonInput(map[string]any{"rows": 3})), // iter 1: denied, no orientation phase to precede it
			toolStep("advisor", `{}`),                                      // iter 2: forced to consult before it can even retry
			// iter 3 (retry propose_grid) never runs: budget exhausts first.
		)
		advisorLLM := &recordingLLM{}

		h, err := New(&stubLLM{},
			WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
			WithSystemPrompt("test"),
			WithContextFilesDisabled(),
			WithPermissionsDisabled(),
			WithAdvisorLLM(advisorLLM),
			WithBudgets(budgets.Limits{MaxIterations: 2}),
		)
		if err != nil {
			t.Fatalf("New() error: %v", err)
		}
		defer h.Shutdown()

		answer, err := h.RunTurn(context.Background(), "propose a grid")
		if err == nil {
			t.Fatalf("RunTurn succeeded with answer %q, want budget-exhaustion error (this is the incident)", answer)
		}
		if !strings.Contains(err.Error(), "budget exhausted") {
			t.Errorf("error = %q, want it to mention budget exhaustion", err.Error())
		}
		if got := agent.callCount(); got != 2 {
			t.Errorf("agent calls = %d, want 2 (denial + forced advisor consult, then budget cuts it off)", got)
		}
	})

	t.Run("exempt tool fits the same unwidened budget", func(t *testing.T) {
		agent := newScriptedAgent(
			toolStep("propose_grid", jsonInput(map[string]any{"rows": 3})), // iter 1: exempt, runs immediately
			finalStep("grid proposed"),                                     // iter 2: same shape as before advisor was ever wired in
		)
		advisorLLM := &recordingLLM{}

		h, err := New(&stubLLM{},
			WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
			WithSystemPrompt("test"),
			WithContextFilesDisabled(),
			WithPermissionsDisabled(),
			WithAdvisorLLM(advisorLLM),
			WithAdvisorExemptTools("propose_grid"),
			WithBudgets(budgets.Limits{MaxIterations: 2}),
		)
		if err != nil {
			t.Fatalf("New() error: %v", err)
		}
		defer h.Shutdown()

		answer, err := h.RunTurn(context.Background(), "propose a grid")
		if err != nil {
			t.Fatalf("RunTurn error: %v, want success within the unwidened budget", err)
		}
		if answer != "grid proposed" {
			t.Errorf("answer = %q, want %q", answer, "grid proposed")
		}
		if reqs := advisorLLM.requests(); len(reqs) != 0 {
			t.Errorf("advisor consults = %d, want 0", len(reqs))
		}
	})
}

func TestHarnessDisabledToolsRemovesBuiltins(t *testing.T) {
	h := newTestHarness(t, WithDisabledTool("bash"), WithDisabledTool("edit"))
	names := make(map[string]bool)
	for _, def := range h.ToolDefinitions() {
		names[strings.ToLower(def.Name)] = true
	}
	for _, banned := range []string{"bash", "edit"} {
		if names[banned] {
			t.Errorf("tool %q present despite WithDisabledTool", banned)
		}
	}
	for _, required := range []string{"read", "grep", "glob"} {
		if !names[required] {
			t.Errorf("tool %q missing; WithDisabledTool removed too much", required)
		}
	}
}

func TestHarnessCreatesEventBus(t *testing.T) {
	h := newTestHarness(t)
	if h.EventBus() == nil {
		t.Fatal("EventBus() should not be nil")
	}
}

func TestHarnessEmitsTurnEventsOnRunTurn(t *testing.T) {
	h := newTestHarness(t)

	ch := h.EventBus().Subscribe(50)

	if _, err := h.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	var types []core.EventType
	for {
		select {
		case ev := <-ch:
			types = append(types, ev.Type())
		default:
			goto check
		}
	}
check:
	hasType := func(et core.EventType) bool {
		return slices.Contains(types, et)
	}
	if !hasType(core.EventTurnStarted) {
		t.Error("missing TurnStarted event")
	}
	if !hasType(core.EventTurnCompleted) {
		t.Error("missing TurnCompleted event")
	}
}

func TestHarnessDefaultAgentBuilder(t *testing.T) {
	redirectHome(t)
	h, err := New(&stubLLM{}, WithSystemPrompt("test"), WithContextFilesDisabled())
	if err != nil {
		t.Fatalf("New() without WithAgentBuilder error: %v", err)
	}
	if h == nil {
		t.Fatal("New() returned nil harness")
	}
}

func hasTool(h *Harness, name string) bool {
	for _, def := range h.ToolDefinitions() {
		if def.Name == name {
			return true
		}
	}
	return false
}

func TestHarnessRegistersREPLToolByDefault(t *testing.T) {
	h := newTestHarness(t)
	defer h.Shutdown()
	if !hasTool(h, "repl") {
		t.Error("repl tool should be registered by default")
	}
}

func TestWithConversationIDSetsRunnerID(t *testing.T) {
	redirectHome(t)
	configDir, _ := memoryDirs()
	if err := os.WriteFile(filepath.Join(configDir, ".agent_memory-20260710-0900-cafe0001.md"),
		[]byte("# Agent Memory\n\nresume state marker\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Default builder path (no WithAgentBuilder): construction must succeed
	// with a memory file present and adopt the supplied conversation ID.
	h, err := New(&stubLLM{}, WithConversationID("cafe0001"), WithContextFilesDisabled())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Shutdown()
	if h.ConversationID() != "cafe0001" {
		t.Fatalf("ConversationID = %q, want cafe0001", h.ConversationID())
	}
}

func TestCompressionEventPersistsMemory(t *testing.T) {
	redirectHome(t)
	h := newTestHarness(t)
	defer h.Shutdown()

	h.EventBus().Emit(core.ContextCompressedEvent{
		BaseEvent: core.NewBaseEvent(core.EventContextCompressed, h.ConversationID()),
		Summary:   "persisted by subscriber",
	})
	configDir, _ := memoryDirs()
	deadline := time.Now().Add(2 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(configDir, ".agent_memory-*-"+h.ConversationID()+".md"))
		if len(matches) == 1 {
			data, _ := os.ReadFile(matches[0])
			if strings.Contains(string(data), "persisted by subscriber") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber never persisted the compression event")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChildCompressionGoesToCache(t *testing.T) {
	redirectHome(t)
	h := newTestHarness(t)
	defer h.Shutdown()

	childID := h.ConversationID() + "_deadbeef"
	h.EventBus().Emit(core.ContextCompressedEvent{
		BaseEvent: core.NewBaseEvent(core.EventContextCompressed, childID),
		Summary:   "child summary",
	})
	_, cacheDir := memoryDirs()
	deadline := time.Now().Add(2 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(cacheDir, ".agent_memory-*-"+childID+".md"))
		if len(matches) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child compression not persisted to cache dir")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Default-on permissions: bash escalates to AskUser, the approval event
// reaches hooks, and a denial comes back to the model as an error result.
func TestHarnessDefaultPermissionsAskAndDeny(t *testing.T) {
	redirectHome(t)
	agent := newScriptedAgent(
		toolStep("bash", jsonInput(map[string]any{"command": "ls"})),
		finalStep("done"),
	)

	var requested core.ApprovalRequestedEvent
	h, err := New(&stubLLM{},
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithHooks(eventbus.Hooks{
			OnApprovalRequested: func(e core.ApprovalRequestedEvent) {
				requested = e
				e.Respond(false)
			},
		}),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	answer, err := h.RunTurn(context.Background(), "list files")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if answer != "done" {
		t.Errorf("answer = %q", answer)
	}
	if requested.ToolName != "bash" {
		t.Fatalf("approval requested for %q, want bash", requested.ToolName)
	}

	// The denial must reach the model as an error tool result.
	calls := agent.capturedCalls()
	if len(calls) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(calls))
	}
	second := calls[1].Messages
	last := second[len(second)-1]
	found := false
	for _, block := range last.Content {
		if strings.Contains(block.ToolOutput, "denied") {
			found = true
		}
	}
	if !found {
		t.Errorf("tool result does not mention denial: %+v", last.Content)
	}
}

// WithReadOnly: mutating tools (bash) are denied instantly with a
// "read-only mode" error result — no approval event ever fires — while
// read-only-marked tools (Read, advisor, repl — its sandbox blocks file
// writes) and spawn_agent (children carry the same gate) execute normally.
func TestHarnessReadOnlyMode(t *testing.T) {
	redirectHome(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(file, []byte("readable-content"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	agent := newScriptedAgent(
		toolStep("bash", jsonInput(map[string]any{"command": "ls"})),
		toolStep("repl", jsonInput(map[string]any{"code": "1"})),
		toolStep("Read", jsonInput(map[string]any{"file_path": file})),
		toolStep("advisor", jsonInput(map[string]any{"plan": "a plan"})),
		toolStep("spawn_agent", jsonInput(map[string]any{"task": "child task"})),
		finalStep("done"),
	)

	// First build is the main agent; each spawn builds a child that answers
	// immediately.
	builds := 0
	builder := func(_ common.LLM, _ string) (core.Agent, error) {
		builds++
		if builds == 1 {
			return agent, nil
		}
		return newScriptedAgent(finalStep("child-done")), nil
	}

	approvals := 0
	h, err := New(&stubLLM{},
		WithAgentBuilder(builder),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithAdvisorLLM(&stubLLM{}),
		WithReadOnly(),
		WithHooks(eventbus.Hooks{
			OnApprovalRequested: func(e core.ApprovalRequestedEvent) {
				approvals++
				e.Respond(true)
			},
		}),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	answer, err := h.RunTurn(context.Background(), "do things")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if answer != "done" {
		t.Errorf("answer = %q", answer)
	}
	if approvals != 0 {
		t.Errorf("approval events fired = %d, want 0", approvals)
	}

	// Each call's tool result lands in the next call's last message.
	calls := agent.capturedCalls()
	if len(calls) != 6 {
		t.Fatalf("agent calls = %d, want 6", len(calls))
	}
	lastOutput := func(c capturedCall) string {
		msg := c.Messages[len(c.Messages)-1]
		var out strings.Builder
		for _, block := range msg.Content {
			out.WriteString(block.ToolOutput)
		}
		return out.String()
	}
	if got := lastOutput(calls[1]); !strings.Contains(got, "read-only mode") {
		t.Errorf("bash result = %q, want read-only mode denial", got)
	}
	// repl is marked read-only (sandbox blocks file writes; only in-memory
	// blackboard state mutates), so it executes rather than being denied.
	if got := lastOutput(calls[2]); strings.Contains(got, "read-only mode") {
		t.Errorf("repl result = %q, want execution, not denial", got)
	}
	if got := lastOutput(calls[3]); !strings.Contains(got, "readable-content") {
		t.Errorf("Read result = %q, want file content", got)
	}
	if got := lastOutput(calls[4]); strings.Contains(got, "read-only mode") {
		t.Errorf("advisor result = %q, want execution, not denial", got)
	}
	if got := lastOutput(calls[5]); !strings.Contains(got, "child-done") {
		t.Errorf("spawn_agent result = %q, want child answer", got)
	}
}

// In read-only mode a spawned child loop carries the same gate: its
// mutating tool calls are denied even though the parent's spawn ran.
func TestHarnessReadOnlyModeGatesSubagentTools(t *testing.T) {
	redirectHome(t)
	dir := t.TempDir()

	parent := newScriptedAgent(
		toolStep("spawn_agent", jsonInput(map[string]any{"task": "write a file"})),
		finalStep("done"),
	)
	child := newScriptedAgent(
		toolStep("write", jsonInput(map[string]any{"file_path": dir + "/out.txt", "content": "x"})),
		finalStep("child-done"),
	)

	builds := 0
	builder := func(_ common.LLM, _ string) (core.Agent, error) {
		builds++
		if builds == 1 {
			return parent, nil
		}
		return child, nil
	}

	h, err := New(&stubLLM{},
		WithAgentBuilder(builder),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithReadOnly(),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	if _, err := h.RunTurn(context.Background(), "delegate"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	if _, err := os.Stat(dir + "/out.txt"); !os.IsNotExist(err) {
		t.Errorf("child wrote the file despite read-only mode (stat err = %v)", err)
	}
	childCalls := child.capturedCalls()
	if len(childCalls) != 2 {
		t.Fatalf("child agent calls = %d, want 2", len(childCalls))
	}
	msg := childCalls[1].Messages[len(childCalls[1].Messages)-1]
	var out strings.Builder
	for _, block := range msg.Content {
		out.WriteString(block.ToolOutput)
	}
	if !strings.Contains(out.String(), "read-only mode") {
		t.Errorf("child write result = %q, want read-only mode denial", out.String())
	}
}

// WithPermissionsDisabled: bash runs unquestioned.
func TestHarnessPermissionsDisabledRunsToolsDirectly(t *testing.T) {
	redirectHome(t)
	dir := t.TempDir()
	agent := newScriptedAgent(
		toolStep("bash", jsonInput(map[string]any{"command": "echo hi > " + dir + "/out.txt"})),
		finalStep("done"),
	)

	h, err := New(&stubLLM{},
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		WithPermissionsDisabled(),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer h.Shutdown()

	if _, err := h.RunTurn(context.Background(), "write it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	assertFileContains(t, dir+"/out.txt", "hi")
}
