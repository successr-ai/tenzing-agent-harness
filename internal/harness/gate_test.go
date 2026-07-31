package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// A gate error blocks the tool call; its message is fed back to the model
// as the tool result.
func TestToolCallGateBlocksExecution(t *testing.T) {
	dir := t.TempDir()
	scripted := newScriptedAgent(
		toolStep("bash", jsonInput(map[string]any{"command": "echo hi > " + dir + "/out.txt"})),
		finalStep("done"),
	)
	var gated []string
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPermissionsDisabled(),
		WithToolCallGate(func(_ context.Context, call core.ToolCall) error {
			gated = append(gated, call.Name)
			return fmt.Errorf("blocked by test gate")
		}),
	)
	t.Cleanup(h.Shutdown)

	if _, err := h.RunTurn(context.Background(), "write it"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(gated) == 0 || gated[0] != "bash" {
		t.Fatalf("gate calls = %v, want [bash ...]", gated)
	}
	assertFileNotExists(t, dir+"/out.txt")

	// The blocked result reaches the model as a tool result.
	calls := scripted.capturedCalls()
	if len(calls) < 2 {
		t.Fatalf("agent calls = %d, want 2", len(calls))
	}
	var second strings.Builder
	for _, m := range calls[1].Messages {
		for _, b := range m.Content {
			second.WriteString(b.Text)
			second.WriteString(b.ToolOutput)
		}
	}
	if !strings.Contains(second.String(), "blocked by test gate") {
		t.Errorf("gate error not fed back to model:\n%s", second.String())
	}
}
