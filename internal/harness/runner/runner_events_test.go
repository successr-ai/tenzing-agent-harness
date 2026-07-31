package runner

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type eventCollector struct {
	mu     sync.Mutex
	events []core.Event
}

func (c *eventCollector) Emit(ev core.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *eventCollector) byType(t core.EventType) []core.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []core.Event
	for _, ev := range c.events {
		if ev.Type() == t {
			out = append(out, ev)
		}
	}
	return out
}

type minimalAgent struct {
	mu       sync.Mutex
	steps    []core.ReasoningResult
	idx      int
	messages [][]common.Message
}

func (a *minimalAgent) GetCurrentModel() string               { return "" }
func (a *minimalAgent) UpdateStreamCallback(_ func(string))   {}
func (a *minimalAgent) UpdateThinkingCallback(_ func(string)) {}

func (a *minimalAgent) DoReasoning(_ context.Context, messages []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, messages)
	r := a.steps[a.idx]
	a.idx++
	return r, nil
}

// newTestContextStore returns a fresh, isolated in-memory context store for
// tests that just need RunLoop to have somewhere to build history.
func newTestContextStore() core.ContextPort {
	return contextstore.New(contextstore.Config{})
}

// toolCallStep builds a ReasoningResult for a tool_use response, keeping
// ToolCalls and Meta.AssistantMessage in sync — as the real agent does. The
// context store pairs tool_results against the message's own tool_use
// blocks, not the parallel ToolCalls list, so tests must supply both.
func toolCallStep(calls ...core.ToolCall) core.ReasoningResult {
	blocks := make([]common.ContentBlock, len(calls))
	for i, c := range calls {
		blocks[i] = common.NewToolUseContent(c.ID, c.Name, []byte(c.Input))
	}
	return core.ReasoningResult{
		ToolCalls: calls,
		Meta:      core.ResponseMeta{AssistantMessage: common.Message{Role: common.RoleAssistant, Content: blocks}},
	}
}

func TestRunnerEmitsTurnAndLoopEvents(t *testing.T) {
	collector := &eventCollector{}

	agent := &minimalAgent{steps: []core.ReasoningResult{
		{FinalAnswer: "done"},
	}}

	r, err := NewAgentRunner(
		agent,
		WithEmitter(collector),
		WithToolRegistry(toolport.NewRegistry("")),
		WithSystemPrompt("test"),
		WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatal(err)
	}

	answer, err := r.RunLoop(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "done" {
		t.Errorf("answer = %q, want %q", answer, "done")
	}

	if len(collector.byType(core.EventTurnStarted)) != 1 {
		t.Errorf("expected 1 TurnStarted, got %d", len(collector.byType(core.EventTurnStarted)))
	}
	if len(collector.byType(core.EventLoopStarted)) != 1 {
		t.Errorf("expected 1 LoopStarted, got %d", len(collector.byType(core.EventLoopStarted)))
	}
	if len(collector.byType(core.EventReasoningStarted)) != 1 {
		t.Errorf("expected 1 ReasoningStarted, got %d", len(collector.byType(core.EventReasoningStarted)))
	}
	if len(collector.byType(core.EventReasoningFinished)) != 1 {
		t.Errorf("expected 1 ReasoningFinished, got %d", len(collector.byType(core.EventReasoningFinished)))
	}
	if len(collector.byType(core.EventLoopStopped)) != 1 {
		t.Errorf("expected 1 LoopStopped, got %d", len(collector.byType(core.EventLoopStopped)))
	}
	if len(collector.byType(core.EventTurnCompleted)) != 1 {
		t.Errorf("expected 1 TurnCompleted, got %d", len(collector.byType(core.EventTurnCompleted)))
	}
}

func TestRunnerEmitsToolEvents(t *testing.T) {
	collector := &eventCollector{}

	registry := toolport.NewRegistry("")
	registry.Register(&echoTool{})

	agent := &minimalAgent{steps: []core.ReasoningResult{
		toolCallStep(core.ToolCall{ID: "1", Name: "echo", Input: `{"text":"hi"}`}),
		{FinalAnswer: "done"},
	}}

	r, err := NewAgentRunner(
		agent,
		WithEmitter(collector),
		WithToolRegistry(registry),
		WithSystemPrompt("test"),
		WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.RunLoop(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}

	if len(collector.byType(core.EventToolExecutionStarted)) != 1 {
		t.Errorf("expected 1 ToolExecutionStarted, got %d", len(collector.byType(core.EventToolExecutionStarted)))
	}
	if len(collector.byType(core.EventToolSucceeded)) != 1 {
		t.Errorf("expected 1 ToolSucceeded, got %d", len(collector.byType(core.EventToolSucceeded)))
	}
	if len(collector.byType(core.EventToolExecutionFinished)) != 1 {
		t.Errorf("expected 1 ToolExecutionFinished, got %d", len(collector.byType(core.EventToolExecutionFinished)))
	}
}

// TestRunnerExecutesAllToolCallsInBatch verifies that when the model issues
// several tool calls in one response, every call is executed and all results
// are fed back to the next reasoning cycle in order — none may be dropped
// with a "tool call was not executed" placeholder.
func TestRunnerExecutesAllToolCallsInBatch(t *testing.T) {
	collector := &eventCollector{}

	registry := toolport.NewRegistry("")
	registry.Register(&echoTool{})

	agent := &minimalAgent{steps: []core.ReasoningResult{
		toolCallStep(
			core.ToolCall{ID: "1", Name: "echo", Input: `{"text":"one"}`},
			core.ToolCall{ID: "2", Name: "echo", Input: `{"text":"two"}`},
			core.ToolCall{ID: "3", Name: "echo", Input: `{"text":"three"}`},
		),
		{FinalAnswer: "done"},
	}}

	r, err := NewAgentRunner(
		agent,
		WithEmitter(collector),
		WithToolRegistry(registry),
		WithSystemPrompt("test"),
		WithContextStore(newTestContextStore()),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.RunLoop(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	if got := len(collector.byType(core.EventToolExecutionStarted)); got != 3 {
		t.Errorf("expected 3 ToolExecutionStarted, got %d", got)
	}
	if got := len(collector.byType(core.EventToolSucceeded)); got != 3 {
		t.Errorf("expected 3 ToolSucceeded, got %d", got)
	}

	if len(agent.messages) != 2 {
		t.Fatalf("expected 2 reasoning calls, got %d", len(agent.messages))
	}
	// The second reasoning call must see the tool_result message with all
	// three outputs paired by id, in order — none dropped or replaced with a
	// placeholder.
	second := agent.messages[1]
	last := second[len(second)-1]
	if last.Role != common.RoleTool {
		t.Fatalf("last message role = %q, want tool", last.Role)
	}
	want := []string{`echo: {"text":"one"}`, `echo: {"text":"two"}`, `echo: {"text":"three"}`}
	if len(last.Content) != len(want) {
		t.Fatalf("tool_result blocks = %d, want %d: %+v", len(last.Content), len(want), last.Content)
	}
	for i := range want {
		if last.Content[i].ToolOutput != want[i] {
			t.Errorf("tool_result[%d].ToolOutput = %q, want %q", i, last.Content[i].ToolOutput, want[i])
		}
	}
}

type echoTool struct{}

func (e *echoTool) Name() string        { return "echo" }
func (e *echoTool) Description() string { return "echoes input" }
func (e *echoTool) Schema() tooldef.Schema {
	return tooldef.Schema{Properties: map[string]tooldef.SchemaProperty{"text": {Type: tooldef.JsonTypeString}}}
}
func (e *echoTool) Execute(_ context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	return tooldef.NewToolResult("echo: " + exctx.Arguments[0]), nil
}

// countingEchoTool tracks how many times Execute is called, so tests can
// assert a denied tool call never reaches the underlying tool.
type countingEchoTool struct {
	mu      sync.Mutex
	invoked int
}

func (c *countingEchoTool) Name() string        { return "echo" }
func (c *countingEchoTool) Description() string { return "echoes input" }
func (c *countingEchoTool) Schema() tooldef.Schema {
	return tooldef.Schema{Properties: map[string]tooldef.SchemaProperty{"text": {Type: tooldef.JsonTypeString}}}
}
func (c *countingEchoTool) Execute(_ context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	c.mu.Lock()
	c.invoked++
	c.mu.Unlock()
	return tooldef.NewToolResult("echo: " + exctx.Arguments[0]), nil
}

func (c *countingEchoTool) invocations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invoked
}

// denyExt is a test extension that denies every tool call.
type denyExt struct{}

func (denyExt) Name() string { return "deny-all" }
func (denyExt) OnToolCall(ctx context.Context, tcc *core.ToolCallContext) error {
	tcc.Decision = core.Deny
	tcc.Reason = "test policy"
	return nil
}

// TestToolCallDeniedByExtension verifies that when a registered extension
// denies a tool call, the loop feeds back a denied-by-policy error result
// without ever invoking the underlying tool.
func TestToolCallDeniedByExtension(t *testing.T) {
	collector := &eventCollector{}

	registry := toolport.NewRegistry("")
	tool := &countingEchoTool{}
	registry.Register(tool)

	agent := &minimalAgent{steps: []core.ReasoningResult{
		toolCallStep(core.ToolCall{ID: "1", Name: "echo", Input: `{"text":"hi"}`}),
		{FinalAnswer: "done"},
	}}

	r, err := NewAgentRunner(
		agent,
		WithEmitter(collector),
		WithToolRegistry(registry),
		WithSystemPrompt("test"),
		WithContextStore(newTestContextStore()),
		WithExtensions(core.NewExtensions(denyExt{})),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.RunLoop(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	if got := tool.invocations(); got != 0 {
		t.Errorf("expected tool to never execute, invoked %d times", got)
	}

	failed := collector.byType(core.EventToolFailed)
	if len(failed) != 1 {
		t.Fatalf("expected 1 ToolFailedEvent, got %d", len(failed))
	}
	ev, ok := failed[0].(core.ToolFailedEvent)
	if !ok {
		t.Fatalf("event is not ToolFailedEvent: %T", failed[0])
	}
	if !strings.Contains(ev.Error, "denied by policy") {
		t.Errorf("error = %q, want substring %q", ev.Error, "denied by policy")
	}
}
