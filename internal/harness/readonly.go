package harness

import (
	"context"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

var (
	_ core.Extension    = (*readOnlyExt)(nil)
	_ core.ToolCallHook = (*readOnlyExt)(nil)
)

// readOnlyExt denies every tool call whose tool is not marked read-only,
// per the composite ToolPort's ReadOnly classification (unknown or unmarked
// tools classify as mutating). classify is late-bound: the composite is
// built after the extension set, so harness.New assigns it once the
// composite exists — before any turn runs. The same instance is shared with
// every subagent loop.
//
// spawn_agent is allowed by name despite carrying no ReadOnly marker (in
// normal mode its children mutate freely, so a global marker would lie):
// in read-only mode every child loop carries this same hook, so a spawned
// agent cannot touch the filesystem — only shared in-memory blackboard
// state changes.
type readOnlyExt struct {
	classify func(name string) bool
}

func (e *readOnlyExt) Name() string { return "read-only" }

func (e *readOnlyExt) OnToolCall(_ context.Context, tcc *core.ToolCallContext) error {
	name := strings.ToLower(tcc.Call.Name)
	if name == "spawn_agent" || (e.classify != nil && e.classify(name)) {
		return nil
	}
	if tcc.Decision < core.Deny {
		tcc.Decision = core.Deny
		tcc.Reason = "read-only mode: tool is not read-only"
	}
	return nil
}
