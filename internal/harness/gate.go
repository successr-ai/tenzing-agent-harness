package harness

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// ToolCallGate is a pre-execution veto consulted before every tool call. A
// non-nil error blocks the call and is fed back to the model as the tool
// result.
type ToolCallGate func(ctx context.Context, call core.ToolCall) error

var (
	_ core.Extension    = (*toolCallGateExt)(nil)
	_ core.ToolCallHook = (*toolCallGateExt)(nil)
)

// toolCallGateExt adapts a ToolCallGate into a core.Extension so the same
// gate instance runs in the main loop and every subagent loop.
type toolCallGateExt struct {
	gate ToolCallGate
}

func (e *toolCallGateExt) Name() string { return "tool-call-gate" }

func (e *toolCallGateExt) OnToolCall(ctx context.Context, tcc *core.ToolCallContext) error {
	return e.gate(ctx, *tcc.Call)
}
