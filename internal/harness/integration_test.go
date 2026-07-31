package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/reminders"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/runner"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// ---------------------------------------------------------------------------
// Scenario 1: File tools — Read, Edit, Write (read-before-edit enforced by
// the registry's shared FileTracker)
//
// All tools parse JSON from Arguments[0], matching the format that LLMs
// produce. Tests exercise both direct registry calls and full agent loop.
// ---------------------------------------------------------------------------

// readFirst runs Read on path so tracker-enforcing Edit/Write calls pass.
func readFirst(t *testing.T, registry *toolport.Registry, path string) {
	t.Helper()
	res, err := registry.Execute(context.Background(), "Read", jsonInput(map[string]any{"file_path": path}))
	if err != nil || res.IsError {
		t.Fatalf("Read failed: err=%v result=%s", err, res.Output)
	}
}

func TestIntegration_ReadTool(t *testing.T) {
	workDir := t.TempDir()
	content := "line one\nline two\nline three\n"
	filePath := seedFile(t, workDir, "sample.txt", content)

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	result, err := registry.Execute(context.Background(), "Read", jsonInput(map[string]any{
		"file_path": filePath,
	}))
	if err != nil {
		t.Fatalf("Read tool error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Read returned error: %s", result.Output)
	}

	assertAnswerContains(t, result.Output, "line one")
	assertAnswerContains(t, result.Output, "line two")
	assertAnswerContains(t, result.Output, "line three")
}

func TestIntegration_ReadTool_MissingFile(t *testing.T) {
	workDir := t.TempDir()
	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	result, err := registry.Execute(context.Background(), "Read", jsonInput(map[string]any{
		"file_path": filepath.Join(workDir, "nope.txt"),
	}))
	if err != nil {
		t.Fatalf("Read tool error: %v", err)
	}
	if !result.IsError {
		t.Fatal("Read should return error for missing file")
	}
}

func TestIntegration_WriteTool(t *testing.T) {
	workDir := t.TempDir()
	original := "original content"
	filePath := seedFile(t, workDir, "target.txt", original)

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	// Overwriting an existing file requires Reading it first.
	readFirst(t, registry, filePath)
	writeResult, err := registry.Execute(context.Background(), "Write", jsonInput(map[string]any{
		"file_path": filePath,
		"content":   "new content",
	}))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if writeResult.IsError {
		t.Fatalf("Write returned error: %s", writeResult.Output)
	}
	assertFileEquals(t, filePath, "new content")

	// New files need no prior Read; parent directories are created.
	newPath := filepath.Join(workDir, "sub", "fresh.txt")
	writeResult, err = registry.Execute(context.Background(), "Write", jsonInput(map[string]any{
		"file_path": newPath,
		"content":   "fresh",
	}))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if writeResult.IsError {
		t.Fatalf("Write returned error: %s", writeResult.Output)
	}
	assertFileEquals(t, newPath, "fresh")
}

func TestIntegration_WriteTool_OverwriteUnread(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "target.txt", "content")

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	result, err := registry.Execute(context.Background(), "Write", jsonInput(map[string]any{
		"file_path": filePath,
		"content":   "clobber",
	}))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if !result.IsError {
		t.Fatal("Write over an unread existing file should return error")
	}
	assertAnswerContains(t, result.Output, "read")
	assertFileEquals(t, filePath, "content")
}

func TestIntegration_EditTool(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "editable.txt", "hello world")

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	readFirst(t, registry, filePath)
	result, err := registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":  filePath,
		"old_string": "hello",
		"new_string": "goodbye",
	}))
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Edit returned error: %s", result.Output)
	}
	assertFileEquals(t, filePath, "goodbye world")
}

func TestIntegration_EditTool_NotFound(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "editable.txt", "hello world")

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	result, err := registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":  filePath,
		"old_string": "MISSING",
		"new_string": "nope",
	}))
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if !result.IsError {
		t.Fatal("Edit should error when old_string not found")
	}
}

func TestIntegration_EditTool_NotUnique(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "editable.txt", "aaa bbb aaa")

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	readFirst(t, registry, filePath)
	result, err := registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":  filePath,
		"old_string": "aaa",
		"new_string": "ccc",
	}))
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if !result.IsError {
		t.Fatal("Edit should error when old_string is not unique")
	}
	assertAnswerContains(t, result.Output, "not unique")
}

func TestIntegration_EditTool_ReplaceAll(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "editable.txt", "aaa bbb aaa")

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	readFirst(t, registry, filePath)
	result, err := registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":   filePath,
		"old_string":  "aaa",
		"new_string":  "ccc",
		"replace_all": true,
	}))
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Edit returned error: %s", result.Output)
	}
	assertFileEquals(t, filePath, "ccc bbb ccc")
}

func TestIntegration_ReadWriteEdit_FullCycle(t *testing.T) {
	workDir := t.TempDir()
	original := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	filePath := seedFile(t, workDir, "main.go", original)

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	// Step 1: Read stamps the tracker
	readFirst(t, registry, filePath)

	// Step 2: Write overwrites (verified fresh by the tracker)
	res, err := registry.Execute(context.Background(), "Write", jsonInput(map[string]any{
		"file_path": filePath,
		"content":   "func main() {\n\tfmt.Println(\"modified\")\n}\n",
	}))
	if err != nil || res.IsError {
		t.Fatalf("Write failed: err=%v result=%s", err, res.Output)
	}
	assertFileContains(t, filePath, "modified")

	// Step 3: Edit works without a fresh Read — Write re-stamped the tracker
	res, err = registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":  filePath,
		"old_string": "modified",
		"new_string": "changed-again",
	}))
	if err != nil || res.IsError {
		t.Fatalf("Edit failed: err=%v result=%s", err, res.Output)
	}
	assertFileContains(t, filePath, "changed-again")

	// Step 4: external modification invalidates the stamp — Edit is rejected
	if err := os.WriteFile(filePath, []byte("changed externally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = registry.Execute(context.Background(), "Edit", jsonInput(map[string]any{
		"file_path":  filePath,
		"old_string": "changed",
		"new_string": "nope",
	}))
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if !res.IsError {
		t.Fatal("Edit after external change should return error")
	}
	assertAnswerContains(t, res.Output, "changed since")
}

// Full agent loop: Read → Write → Edit, all through RunLoop
func TestIntegration_ReadWriteEdit_ThroughLoop(t *testing.T) {
	workDir := t.TempDir()
	original := "hello world\n"
	filePath := seedFile(t, workDir, "loopfile.txt", original)

	agent := newScriptedAgent(
		toolStep("Read", jsonInput(map[string]any{"file_path": filePath})),
		toolStep("Write", jsonInput(map[string]any{"file_path": filePath, "content": "goodbye world\n"})),
		toolStep("Edit", jsonInput(map[string]any{"file_path": filePath, "old_string": "goodbye", "new_string": "farewell"})),
		finalStep("edited"),
	)

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	runner, err := runner.NewAgentRunner(
		agent,
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	answer, err := runner.RunLoop(context.Background(), "read, write, then edit")
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if answer != "edited" {
		t.Errorf("answer = %q, want %q", answer, "edited")
	}

	assertFileEquals(t, filePath, "farewell world\n")
	assertCallCount(t, agent, 4)
}

// ---------------------------------------------------------------------------
// Agent loop mechanics
// ---------------------------------------------------------------------------

func TestIntegration_FinalAnswerOnly(t *testing.T) {
	agent := newScriptedAgent(finalStep("direct answer"))

	registry := toolport.NewRegistry("")
	runner, err := runner.NewAgentRunner(
		agent,
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	answer, err := runner.RunLoop(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if answer != "direct answer" {
		t.Errorf("answer = %q, want %q", answer, "direct answer")
	}
	assertCallCount(t, agent, 1)
}

func TestIntegration_ContextCanceled(t *testing.T) {
	agent := newScriptedAgent(finalStep("should not reach"))

	registry := toolport.NewRegistry("")
	runner, err := runner.NewAgentRunner(
		agent,
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = runner.RunLoop(ctx, "hi")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestIntegration_UnknownTool(t *testing.T) {
	agent := newScriptedAgent(
		toolStep("nonexistent_tool", "{}"),
	)

	registry := toolport.NewRegistry("")
	runner, err := runner.NewAgentRunner(
		agent,
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	// Unknown tool returns a tool-level error result, agent gets it as input
	// ScriptedAgent has no more steps after the tool call, so it errors
	_, err = runner.RunLoop(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestIntegration_MultipleToolCalls(t *testing.T) {
	workDir := t.TempDir()
	fileA := seedFile(t, workDir, "a.txt", "content A")
	fileB := seedFile(t, workDir, "b.txt", "content B")
	fileC := seedFile(t, workDir, "c.txt", "content C")

	// Three Read tool calls then final answer. Captured so the test can
	// assert on the exact tool_use id the context store must pair against.
	step1 := toolStep("Read", jsonInput(map[string]any{"file_path": fileA}))
	agent := newScriptedAgent(
		step1,
		toolStep("Read", jsonInput(map[string]any{"file_path": fileB})),
		toolStep("Read", jsonInput(map[string]any{"file_path": fileC})),
		finalStep("read 3 files"),
	)

	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	r, err := runner.NewAgentRunner(
		agent,
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	answer, err := r.RunLoop(context.Background(), "read 3 files")
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}
	if answer != "read 3 files" {
		t.Errorf("answer = %q, want %q", answer, "read 3 files")
	}

	// 4 agent calls: initial + 3 Read results
	assertCallCount(t, agent, 4)

	// The second reasoning call is the real proof this goes through the
	// context store correctly: it must see the user turn, the assistant's
	// tool_use response (step1), and a RoleTool message whose tool_result is
	// paired to that exact tool_use id — not dropped as an unpaired
	// placeholder (the bug the zero-value Meta.AssistantMessage fixture used
	// to hide, since the store pairs tool_results against the assistant
	// message's own tool_use blocks, not the parallel ToolCalls list).
	calls := agent.capturedCalls()
	if len(calls) != 4 {
		t.Fatalf("captured calls = %d, want 4", len(calls))
	}
	msgs := calls[1].Messages
	if len(msgs) != 3 {
		t.Fatalf("iteration 2 messages = %d, want 3 (user, assistant tool_use, tool result); got %+v", len(msgs), msgs)
	}
	if msgs[0].Role != common.RoleUser {
		t.Errorf("messages[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != common.RoleAssistant {
		t.Fatalf("messages[1].Role = %q, want assistant", msgs[1].Role)
	}
	wantID := step1.ToolCalls[0].ID
	if len(msgs[1].Content) != 1 || msgs[1].Content[0].Type != common.ContentTypeToolUse || msgs[1].Content[0].ToolUseID != wantID {
		t.Fatalf("messages[1] tool_use = %+v, want a single tool_use block with id %q", msgs[1].Content, wantID)
	}
	if msgs[2].Role != common.RoleTool {
		t.Fatalf("messages[2].Role = %q, want tool", msgs[2].Role)
	}
	if len(msgs[2].Content) != 1 || msgs[2].Content[0].Type != common.ContentTypeToolResult || msgs[2].Content[0].ToolResultID != wantID {
		t.Fatalf("messages[2] tool_result = %+v, want a single tool_result block paired to id %q", msgs[2].Content, wantID)
	}
	if !strings.Contains(msgs[2].Content[0].ToolOutput, "content A") {
		t.Errorf("messages[2] tool_result output = %q, want it to contain the Read output for file A", msgs[2].Content[0].ToolOutput)
	}
}

func TestIntegration_ToolHookCalled(t *testing.T) {
	workDir := t.TempDir()
	filePath := seedFile(t, workDir, "hook.txt", "hook content")

	agent := newScriptedAgent(
		toolStep("Read", jsonInput(map[string]any{"file_path": filePath})),
		finalStep("done"),
	)

	collector := &testEventCollector{}
	registry := toolport.NewRegistry("")
	for _, def := range builtins.Defaults() {
		registry.Register(def)
	}

	r, err := runner.NewAgentRunner(
		agent,
		runner.WithEmitter(collector),
		runner.WithToolRegistry(registry),
		runner.WithSystemPrompt("test"),
		runner.WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner error: %v", err)
	}

	_, err = r.RunLoop(context.Background(), "read file")
	if err != nil {
		t.Fatalf("RunLoop error: %v", err)
	}

	succeeded := collector.byType(core.EventToolSucceeded)
	if len(succeeded) != 1 {
		t.Errorf("expected 1 ToolSucceeded event, got %d", len(succeeded))
	} else if ev, ok := succeeded[0].(core.ToolSucceededEvent); !ok || ev.ToolName != "Read" {
		t.Errorf("expected ToolSucceeded for Read, got %v", succeeded[0])
	}
}

// Two runners in the same process: each agent's injected system reminder
// contains only its own plan. Regression for the shared-.agent_todo.json bug
// (docs/SPEC_TODO_PER_RUNNER_ISOLATION.md): runner B previously saw runner
// A's plan in its reminder, and vice versa.
func TestTodoRemindersScopedPerRunner(t *testing.T) {
	newTodoRunner := func(store *todo.TodoFile, agent *ScriptedAgent) *runner.AgentRunner {
		t.Helper()
		registry := toolport.NewRegistry("")
		registry.RegisterFromProvider(store)
		exts := core.NewExtensions(reminders.New(store.FormatReminder))
		r, err := runner.NewAgentRunner(
			agent,
			runner.WithToolRegistry(registry),
			runner.WithExtensions(exts),
			runner.WithSystemPrompt("test"),
			runner.WithContextStore(newTestContextStore()),
		)
		if err != nil {
			t.Fatalf("NewAgentRunner error: %v", err)
		}
		return r
	}

	storeA := todo.NewTodoStore()
	storeB := todo.NewTodoStore()

	agentA := newScriptedAgent(
		toolStep("TodoWrite", jsonInput(map[string]any{"tasks": []map[string]any{{"task": "alpha work"}}})),
		finalStep("done A"),
	)
	agentB := newScriptedAgent(
		toolStep("TodoWrite", jsonInput(map[string]any{"tasks": []map[string]any{{"task": "beta work"}}})),
		finalStep("done B"),
	)

	runnerA := newTodoRunner(storeA, agentA)
	runnerB := newTodoRunner(storeB, agentB)

	// A writes its plan first, then B runs. With the old shared file, B's
	// very first reminder would already contain A's "alpha work" plan.
	if _, err := runnerA.RunLoop(context.Background(), "plan A"); err != nil {
		t.Fatalf("runner A: %v", err)
	}
	if _, err := runnerB.RunLoop(context.Background(), "plan B"); err != nil {
		t.Fatalf("runner B: %v", err)
	}

	for i, call := range agentB.capturedCalls() {
		joined := strings.Join(call.Reminders, "\n")
		if strings.Contains(joined, "alpha work") {
			t.Errorf("runner B call %d reminder leaked runner A's plan:\n%s", i, joined)
		}
	}

	// A's second call (after its TodoWrite) must see its own plan.
	callsA := agentA.capturedCalls()
	if len(callsA) < 2 {
		t.Fatalf("agent A captured %d calls, want >= 2", len(callsA))
	}
	joinedA := strings.Join(callsA[1].Reminders, "\n")
	if !strings.Contains(joinedA, "alpha work") {
		t.Errorf("runner A's reminder missing its own plan:\n%s", joinedA)
	}
	if strings.Contains(joinedA, "beta work") {
		t.Errorf("runner A's reminder leaked runner B's plan:\n%s", joinedA)
	}

	// And B's write must not have clobbered A's store.
	if r := storeA.FormatReminder(); !strings.Contains(r, "alpha work") || strings.Contains(r, "beta work") {
		t.Errorf("store A contaminated after B's write:\n%s", r)
	}
}

// ---------------------------------------------------------------------------
// Memory resume path
//
// The main agent's brain never sees the LLM directly (it's driven entirely
// by the ScriptedAgent's scripted steps) but the harness's contextstore
// still shares the harness's real LLM client for compression summaries — so
// a fake LLM with a tiny context window and a canned summary response drives
// real compression through the real contextstore, and the real
// ContextCompressedEvent -> memory.go persistence path, with no test-only
// shortcut. This is preferred over emitting a synthetic
// ContextCompressedEvent directly on the bus: that would only exercise the
// persist/load halves already covered by TestCompressionEventPersistsMemory
// and TestWithConversationIDSetsRunnerID in harness_test.go, and would skip
// the one hop those tests can't reach — the real contextstore compressor
// actually deciding to compress and emitting the event itself.
// ---------------------------------------------------------------------------

// resumeFakeLLM is a minimal common.LLM used only by the contextstore's
// compressor (the scripted agent never calls an LLM directly). A tiny
// context window makes the compression threshold trivially small so a
// handful of tool round-trips is enough to trigger it; SendSyncMessage
// always returns the same canned summary regardless of the transcript it's
// asked to summarize.
type resumeFakeLLM struct {
	summaryText   string
	contextWindow int
}

func (f *resumeFakeLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{Content: []common.ContentBlock{common.NewTextContent(f.summaryText)}}, nil
}
func (f *resumeFakeLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, _ chan<- common.StreamEvent) error {
	return nil
}
func (f *resumeFakeLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (f *resumeFakeLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (f *resumeFakeLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (f *resumeFakeLLM) GetCurrentModel() string                                  { return "resume-fake-model" }
func (f *resumeFakeLLM) GetContextWindowSize() int                                { return f.contextWindow }

func (f *resumeFakeLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "resume-model", ContextWindowSize: 128000, SupportsVision: true}
}

func TestIntegration_ResumeSeedsContextStoreFromPersistedMemory(t *testing.T) {
	redirectHome(t)

	const wantSummary = "resume smoke test summary: read several files, nothing written"
	compressionLLM := &resumeFakeLLM{summaryText: wantSummary, contextWindow: 1}

	// Enough tool round-trips (each is two appended messages: the assistant
	// tool_use, then the tool result) to push the history past the
	// compressor's KeepRecent(6)-message floor, so the real Store triggers a
	// real compression and emits a real ContextCompressedEvent.
	workDir := t.TempDir()
	agent1 := newScriptedAgent(
		toolStep("Read", jsonInput(map[string]any{"file_path": filepath.Join(workDir, "a.txt")})),
		toolStep("Read", jsonInput(map[string]any{"file_path": filepath.Join(workDir, "b.txt")})),
		toolStep("Read", jsonInput(map[string]any{"file_path": filepath.Join(workDir, "c.txt")})),
		toolStep("Read", jsonInput(map[string]any{"file_path": filepath.Join(workDir, "d.txt")})),
		toolStep("Read", jsonInput(map[string]any{"file_path": filepath.Join(workDir, "e.txt")})),
		finalStep("session one done"),
	)

	h1, err := New(compressionLLM,
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent1, nil }),
		WithSystemPrompt("test"),
		WithContextFilesDisabled(),
		// Session persistence would resume the full JSONL transcript; this
		// test pins the memory-summary fallback path.
		WithSessionDisabled(),
	)
	if err != nil {
		t.Fatalf("New (session 1): %v", err)
	}
	convID := h1.ConversationID()

	if _, err := h1.RunTurn(context.Background(), "read five files"); err != nil {
		t.Fatalf("RunTurn (session 1): %v", err)
	}

	// Compression persistence dispatches asynchronously off the event bus
	// (see harness.go's stopMemoryHook / eventbus.StartHooks) — poll like
	// TestCompressionEventPersistsMemory does.
	configDir, _ := memoryDirs()
	deadline := time.Now().Add(2 * time.Second)
	var persisted string
	for {
		matches, _ := filepath.Glob(filepath.Join(configDir, ".agent_memory-*-"+convID+".md"))
		if len(matches) == 1 {
			persisted = matches[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session 1 never persisted a memory file — real compression did not trigger")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h1.Shutdown()

	data, err := os.ReadFile(persisted)
	if err != nil {
		t.Fatalf("read persisted memory file: %v", err)
	}
	if !strings.Contains(string(data), wantSummary) {
		t.Fatalf("persisted memory file does not contain the compressor's summary:\n%s", data)
	}

	// Session 2: resume under session 1's conversation ID. The seam under
	// test is WithConversationID -> harness.New loads the latest memory file
	// -> passes it as contextstore.Config.InitialMemory -> Store.New seeds
	// the synthetic summary/ack exchange as the first two messages, which
	// the agent must see on its very first DoReasoning call.
	agent2 := newScriptedAgent(finalStep("session two done"))
	h2, err := New(&stubLLM{},
		WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent2, nil }),
		WithSystemPrompt("test"),
		WithConversationID(convID),
		WithContextFilesDisabled(),
		WithSessionDisabled(),
	)
	if err != nil {
		t.Fatalf("New (session 2): %v", err)
	}
	defer h2.Shutdown()

	if h2.ConversationID() != convID {
		t.Fatalf("session 2 ConversationID = %q, want %q", h2.ConversationID(), convID)
	}

	if _, err := h2.RunTurn(context.Background(), "continue where we left off"); err != nil {
		t.Fatalf("RunTurn (session 2): %v", err)
	}

	calls := agent2.capturedCalls()
	if len(calls) != 1 {
		t.Fatalf("session 2 agent calls = %d, want 1", len(calls))
	}
	msgs := calls[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("session 2 first-call messages = %d, want 3 (summary seed, ack, new turn); got %+v", len(msgs), msgs)
	}
	if msgs[0].Role != common.RoleUser {
		t.Errorf("messages[0].Role = %q, want user (summary seed)", msgs[0].Role)
	}
	if seedText := common.CombinedText(msgs[0].Content); !strings.Contains(seedText, wantSummary) {
		t.Errorf("messages[0] does not contain the persisted summary %q:\n%s", wantSummary, seedText)
	}
	if msgs[1].Role != common.RoleAssistant {
		t.Errorf("messages[1].Role = %q, want assistant (synthetic ack)", msgs[1].Role)
	}
	if ackText := common.CombinedText(msgs[1].Content); !strings.Contains(ackText, "Understood") {
		t.Errorf("messages[1] missing synthetic ack text: %q", ackText)
	}
	if msgs[2].Role != common.RoleUser {
		t.Errorf("messages[2].Role = %q, want user (new turn)", msgs[2].Role)
	}
	if newText := common.CombinedText(msgs[2].Content); !strings.Contains(newText, "continue where we left off") {
		t.Errorf("messages[2] missing the new turn's text: %q", newText)
	}
}
