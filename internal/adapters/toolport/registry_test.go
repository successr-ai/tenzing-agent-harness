package toolport

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
)

// captureTool records the ExecutionContext it was executed with.
type captureTool struct {
	name  string
	exctx tooldef.ExecutionContext
}

func (c *captureTool) Name() string           { return c.name }
func (c *captureTool) Description() string    { return "capture" }
func (c *captureTool) Schema() tooldef.Schema { return tooldef.Schema{} }
func (c *captureTool) Execute(_ context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	c.exctx = exctx
	return core.ToolResult{Output: "ok"}, nil
}

func TestExecutePassesWorkingDir(t *testing.T) {
	tests := []struct {
		name       string
		workingDir string
	}{
		{"set", "/some/workspace"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(tt.workingDir)
			capture := &captureTool{name: "capture"}
			if err := r.Register(capture); err != nil {
				t.Fatalf("register: %v", err)
			}

			if _, err := r.Execute(context.Background(), "capture", "{}"); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if capture.exctx.WorkingDir != tt.workingDir {
				t.Errorf("WorkingDir = %q, want %q", capture.exctx.WorkingDir, tt.workingDir)
			}
		})
	}
}

func TestCopyWithoutRemovesBuiltins(t *testing.T) {
	r := NewRegistry("/ws")
	for _, def := range builtins.Defaults() {
		r.Register(def)
	}
	filtered := r.CopyWithout("bash", "Edit")

	for _, name := range []string{"bash", "edit"} {
		if _, ok := filtered.tools[name]; ok {
			t.Errorf("tool %q survived CopyWithout", name)
		}
	}
	for _, name := range []string{"read", "grep", "glob"} {
		if _, ok := filtered.tools[name]; !ok {
			t.Errorf("tool %q missing after CopyWithout", name)
		}
	}
	if filtered.workingDir != "/ws" {
		t.Errorf("workingDir = %q, want %q", filtered.workingDir, "/ws")
	}
}
