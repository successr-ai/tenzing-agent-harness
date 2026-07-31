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
}

// DefaultPolicy asks for anything that executes code or writes files — and
// for every MCP-origin tool — and allows read-only or in-memory-only tools
// by default.
func DefaultPolicy() Policy {
	return Policy{
		Ask:        []string{"bash", "write", "edit", "repl", "spawn_agent", "advisor"},
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
}

func New(p Policy) *Ext {
	return &Ext{
		allow:      toSet(p.Allow),
		deny:       toSet(p.Deny),
		ask:        toSet(p.Ask),
		askOrigins: p.AskOrigins,
		def:        p.Default,
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
