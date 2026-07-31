package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// testEventCollector captures emitted events for assertion in tests.
type testEventCollector struct {
	mu   sync.Mutex
	evts []core.Event
}

func (c *testEventCollector) Emit(ev core.Event) {
	c.mu.Lock()
	c.evts = append(c.evts, ev)
	c.mu.Unlock()
}

func (c *testEventCollector) byType(t core.EventType) []core.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []core.Event
	for _, ev := range c.evts {
		if ev.Type() == t {
			out = append(out, ev)
		}
	}
	return out
}

// ScriptedAgent replays a sequence of ReasoningResults in order.
// Each DoReasoning call returns the next scripted result.
// Captures all inputs for assertion.
type ScriptedAgent struct {
	mu        sync.Mutex
	steps     []core.ReasoningResult
	callIndex int
	captured  []capturedCall
}

type capturedCall struct {
	Messages  []common.Message
	Reminders []string
}

func newScriptedAgent(steps ...core.ReasoningResult) *ScriptedAgent {
	return &ScriptedAgent{steps: steps}
}

func (s *ScriptedAgent) GetCurrentModel() string               { return "scripted-model" }
func (s *ScriptedAgent) UpdateStreamCallback(_ func(string))   {}
func (s *ScriptedAgent) UpdateThinkingCallback(_ func(string)) {}

func (s *ScriptedAgent) DoReasoning(_ context.Context, messages []common.Message, reminders []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.captured = append(s.captured, capturedCall{
		Messages:  append([]common.Message{}, messages...),
		Reminders: append([]string{}, reminders...),
	})

	if s.callIndex >= len(s.steps) {
		return core.ReasoningResult{}, fmt.Errorf("ScriptedAgent: no more steps (called %d times, only %d steps)", s.callIndex+1, len(s.steps))
	}

	result := s.steps[s.callIndex]
	s.callIndex++
	return result, nil
}

// newTestContextStore returns a fresh, isolated in-memory context store for
// tests that construct an AgentRunner directly (WithContextStore is
// required).
func newTestContextStore() core.ContextPort {
	return contextstore.New(contextstore.Config{})
}

func (s *ScriptedAgent) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.captured)
}

func (s *ScriptedAgent) capturedCalls() []capturedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedCall, len(s.captured))
	copy(out, s.captured)
	return out
}

// step builders

// toolStepSeq gives each toolStep call a distinct tool_use id — tests never
// run these builders concurrently (they're called during test setup, not
// from DoReasoning), so a plain counter is safe.
var toolStepSeq int

// toolStep builds a ReasoningResult for a single tool_use response, keeping
// ToolCalls and Meta.AssistantMessage in sync — as the real agent does (see
// internal/adapters/agent/agent.go). The context store pairs tool_results against the
// assistant message's own tool_use content blocks, not the parallel
// ToolCalls list, so a fixture that only set ToolCalls silently dropped
// every tool result (empty pending) instead of exercising the real path.
func toolStep(name, input string) core.ReasoningResult {
	toolStepSeq++
	id := fmt.Sprintf("tu-%d", toolStepSeq)
	call := core.ToolCall{ID: id, Name: name, Input: input}
	return core.ReasoningResult{
		ToolCalls: []core.ToolCall{call},
		Meta: core.ResponseMeta{
			AssistantMessage: common.Message{
				Role:    common.RoleAssistant,
				Content: []common.ContentBlock{common.NewToolUseContent(call.ID, call.Name, []byte(call.Input))},
			},
		},
	}
}

// finalStep builds a ReasoningResult for a final-answer response, with
// Meta.AssistantMessage carrying the same text as FinalAnswer — matching
// what the real agent returns (see internal/adapters/agent/agent.go DoReasoning).
func finalStep(answer string) core.ReasoningResult {
	return core.ReasoningResult{
		FinalAnswer: answer,
		Meta: core.ResponseMeta{
			AssistantText:    answer,
			AssistantMessage: common.NewAssistantMessage(answer),
		},
	}
}

func jsonInput(fields map[string]any) string {
	data, _ := json.Marshal(fields)
	return string(data)
}

// assertion helpers

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("file %s does not contain %q\ngot: %s", path, want, string(data))
	}
}

func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("file %s content mismatch\nwant: %q\ngot:  %q", path, want, string(data))
	}
}

func assertFileNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("file %s should not exist", path)
	}
}

func assertCallCount(t *testing.T, agent *ScriptedAgent, want int) {
	t.Helper()
	got := agent.callCount()
	if got != want {
		t.Errorf("agent call count: got %d, want %d", got, want)
	}
}

func assertAnswerContains(t *testing.T, answer, want string) {
	t.Helper()
	if !strings.Contains(answer, want) {
		t.Errorf("answer does not contain %q\ngot: %s", want, answer)
	}
}

func seedFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}
