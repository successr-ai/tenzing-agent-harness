// Package permissions gates tool calls by name: a core.ToolCallHook that
// escalates each call's Decision per a Policy. It never de-escalates — a
// stricter decision from an earlier hook always survives. Registered FIRST
// in the harness's default extension order so later hooks see its decision.
package permissions

import (
	"context"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Policy maps tool names (exact, matched case-insensitively) to decisions.
// Precedence: Deny > Ask > Allow (by name, most specific) > AskOrigins (by
// origin prefix) > Default.
type Policy struct {
	Allow []string
	Deny  []string
	Ask   []string
	// AskOrigins escalates unlisted tools whose mount origin starts with any
	// of these prefixes (e.g. "mcp:" — external servers are untrusted by
	// default).
	AskOrigins []string
	Default    core.Decision // decision for unlisted tools
	// Bash, when non-nil, refines the decision for the bash tool by matching
	// the command itself (see BashRules). It can lower an AskUser to Allow
	// or raise it to Deny, but never overrides a name-level Deny.
	Bash *BashRules
}

// DefaultPolicy asks for anything that executes code or writes files — and
// for every MCP-origin tool — and allows read-only or in-memory-only tools
// by default. advisor is deliberately not listed: it is read-only, and the
// advisor write-gate mandates calling it, which must not hang on (or be
// auto-denied by) an approval prompt in headless mode.
func DefaultPolicy() Policy {
	return Policy{
		Ask:        []string{"bash", "write", "edit", "repl", "spawn_agent"},
		AskOrigins: []string{"mcp:"},
		Default:    core.Allow,
	}
}

var (
	_ core.Extension    = (*Ext)(nil)
	_ core.ToolCallHook = (*Ext)(nil)
)

type Ext struct {
	allow      map[string]struct{}
	deny       map[string]struct{}
	ask        map[string]struct{}
	askOrigins []string
	def        core.Decision
	bash       *BashRules
}

func New(p Policy) *Ext {
	return &Ext{
		allow:      toSet(p.Allow),
		deny:       toSet(p.Deny),
		ask:        toSet(p.Ask),
		askOrigins: p.AskOrigins,
		def:        p.Default,
		bash:       p.Bash,
	}
}

func toSet(names []string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[strings.ToLower(n)] = struct{}{}
	}
	return s
}

func (e *Ext) Name() string { return "permissions" }

// OnToolCall escalates the call's Decision per the policy; it never lowers
// an existing Decision (the hook chain would restore it anyway).
func (e *Ext) OnToolCall(_ context.Context, tcc *core.ToolCallContext) error {
	name := strings.ToLower(tcc.Call.Name)

	decision := e.def
	reason := "permission policy default"
	switch {
	case has(e.deny, name):
		decision = core.Deny
		reason = "tool denied by permission policy"
	case has(e.ask, name):
		decision = core.AskUser
		reason = "tool requires approval by permission policy"
	case has(e.allow, name):
		decision = core.Allow
	case e.matchesAskOrigin(tcc.Origin):
		decision = core.AskUser
		reason = "tool origin requires approval by permission policy"
	}

	// Per-command bash rules refine the name-level decision. A name-level
	// Deny is absolute; otherwise an allowed command drops to Allow (i.e.
	// stops escalating) and a denied one is blocked outright.
	// The tool name is matched case-insensitively: a harness that registers
	// the tool as "Bash" gets the same command rules as one using "bash".
	if e.bash != nil && strings.EqualFold(name, "bash") && decision != core.Deny {
		if d, ok := e.bash.Verdict(bashCommand(tcc.Call.Input)); ok {
			decision = d
			reason = "bash command denied by permission policy"
		}
	}

	if decision > tcc.Decision {
		tcc.Decision = decision
		tcc.Reason = reason
	}
	return nil
}

func has(s map[string]struct{}, name string) bool {
	_, ok := s[name]
	return ok
}

func (e *Ext) matchesAskOrigin(origin string) bool {
	for _, prefix := range e.askOrigins {
		if strings.HasPrefix(origin, prefix) {
			return true
		}
	}
	return false
}
