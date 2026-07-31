package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// recordingFactory records SpawnAgent calls and returns a canned answer.
type recordingFactory struct {
	tasks    []string
	contexts []string
}

func (f *recordingFactory) SpawnAgent(_ context.Context, task, taskContext string) (string, error) {
	f.tasks = append(f.tasks, task)
	f.contexts = append(f.contexts, taskContext)
	return "child done", nil
}

func TestSpawnExtToolsReturnsSpawnAgentSpec(t *testing.T) {
	ext := NewSpawnExt(&recordingFactory{})
	specs := ext.Tools()
	if len(specs) != 1 {
		t.Fatalf("expected 1 tool spec, got %d", len(specs))
	}
	if specs[0].Definition.Name != "spawn_agent" {
		t.Errorf("tool name = %q, want spawn_agent", specs[0].Definition.Name)
	}
	if specs[0].Origin != "extension:subagents" {
		t.Errorf("origin = %q, want extension:subagents", specs[0].Origin)
	}
}

func TestSpawnExtToolCallsFactoryWithTask(t *testing.T) {
	factory := &recordingFactory{}
	ext := NewSpawnExt(factory)
	spec := ext.Tools()[0]

	res := spec.Execute(context.Background(), core.ToolCall{ID: "t1", Name: "spawn_agent", Input: `{"task":"count the files","context":"repo root"}`})
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Output)
	}
	if !strings.Contains(res.Output, "child done") {
		t.Errorf("output = %q, want the factory's answer", res.Output)
	}
	if len(factory.tasks) != 1 || factory.tasks[0] != "count the files" {
		t.Errorf("factory tasks = %v, want [count the files]", factory.tasks)
	}
	if factory.contexts[0] != "repo root" {
		t.Errorf("factory context = %q, want repo root", factory.contexts[0])
	}
}
