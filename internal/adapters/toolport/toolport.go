package toolport

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

type registryPort struct {
	reg *Registry
}

// Wrap adapts a Registry into a core.ToolPort. The Execute method
// converts the registry's (ToolResult, error) return into a single ToolResult
// (error becomes IsError=true), and recovers panics into error results.
func Wrap(reg *Registry) core.ToolPort { return &registryPort{reg: reg} }

func (p *registryPort) BeginTurn(ctx context.Context) {}

func (p *registryPort) Definitions() []common.ToolDefinition { return p.reg.ProviderDefinitions() }

func (p *registryPort) Origin(name string) string { return "native" }

func (p *registryPort) ReadOnly(name string) bool { return p.reg.IsReadOnly(name) }

func (p *registryPort) Execute(ctx context.Context, call core.ToolCall) (result core.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool panicked", "tool", call.Name, "panic", r, "stack", string(debug.Stack()))
			result = core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("tool %q panicked: %v", call.Name, r), IsError: true}
		}
	}()
	res, err := p.reg.Execute(ctx, call.Name, call.Input)
	if err != nil {
		return core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("tool execution failed: %v", err), IsError: true}
	}
	res.ToolUseID = call.ID
	return res
}
