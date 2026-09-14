package harness

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
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
//
// bash is allowed when shell.Analyze classifies every command it runs as
// read-only against the built-in table (no settings.json overrides — this
// is the unattended mode, and it must not widen from a file nobody
// reviewed). Anything else — a mutation, an unknown binary, a parse error,
// a command that runs nothing — is denied as before.
type readOnlyExt struct {
	classify func(name string) bool
}

func (e *readOnlyExt) Name() string { return "read-only" }

func (e *readOnlyExt) OnToolCall(_ context.Context, tcc *core.ToolCallContext) error {
	name := strings.ToLower(tcc.Call.Name)
	if name == "spawn_agent" || (e.classify != nil && e.classify(name)) {
		return nil
	}
	reason := "read-only mode: tool is not read-only"
	if name == "bash" {
		a := shell.Analyze(bashCommand(tcc.Call.Input), shell.Builtin())
		if a.Err == nil && len(a.Segments) > 0 && a.Class().IsRead() {
			return nil
		}
		reason = "read-only mode: " + a.Summary()
	}
	if tcc.Decision < core.Deny {
		tcc.Decision = core.Deny
		tcc.Reason = reason
	}
	return nil
}

// bashCommand pulls the command out of a bash tool call's JSON input;
// unparseable input yields "" (no segments → denied).
//
// ponytail: eight lines duplicated from permissions rather than exporting
// a helper for one caller.
func bashCommand(input string) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ""
	}
	return args.Command
}
