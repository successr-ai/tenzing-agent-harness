package permissions

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func decideOrigin(t *testing.T, p Policy, name, origin string, initial core.Decision) core.Decision {
	t.Helper()
	ext := New(p)
	tcc := &core.ToolCallContext{
		Call:     &core.ToolCall{ID: "c1", Name: name, Input: "{}"},
		Origin:   origin,
		Decision: initial,
	}
	if err := ext.OnToolCall(context.Background(), tcc); err != nil {
		t.Fatalf("OnToolCall: %v", err)
	}
	return tcc.Decision
}

func decideFor(t *testing.T, p Policy, name string, initial core.Decision) core.Decision {
	t.Helper()
	return decideOrigin(t, p, name, "native", initial)
}

func TestPolicyDecisions(t *testing.T) {
	policy := Policy{
		Allow:   []string{"read"},
		Deny:    []string{"nuke"},
		Ask:     []string{"bash"},
		Default: core.Allow,
	}

	tests := []struct {
		name    string
		tool    string
		policy  Policy
		initial core.Decision
		want    core.Decision
	}{
		{"allow listed", "read", policy, core.Allow, core.Allow},
		{"deny listed", "nuke", policy, core.Allow, core.Deny},
		{"ask listed", "bash", policy, core.Allow, core.AskUser},
		{"case insensitive", "BASH", policy, core.Allow, core.AskUser},
		{"unlisted uses default allow", "grep", policy, core.Allow, core.Allow},
		{"unlisted uses default deny", "grep", Policy{Default: core.Deny}, core.Allow, core.Deny},
		{"never de-escalates", "read", policy, core.Deny, core.Deny},
		{"ask does not lower deny", "bash", policy, core.Deny, core.Deny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideFor(t, tt.policy, tt.tool, tt.initial); got != tt.want {
				t.Errorf("decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAskOriginsEscalateByOriginPrefix(t *testing.T) {
	p := Policy{AskOrigins: []string{"mcp:"}, Default: core.Allow}
	if got := decideOrigin(t, p, "mcp__srv__anything", "mcp:srv", core.Allow); got != core.AskUser {
		t.Errorf("mcp-origin tool = %v, want AskUser", got)
	}
	if got := decideOrigin(t, p, "grep", "native", core.Allow); got != core.Allow {
		t.Errorf("native tool = %v, want Allow", got)
	}
	// explicit name listing is more specific than origin matching
	p.Allow = []string{"mcp__srv__safe"}
	if got := decideOrigin(t, p, "mcp__srv__safe", "mcp:srv", core.Allow); got != core.Allow {
		t.Errorf("explicitly allowed mcp tool = %v, want Allow", got)
	}
}

func TestDefaultPolicyAsksForMCPOrigins(t *testing.T) {
	if got := decideOrigin(t, DefaultPolicy(), "mcp__srv__tool", "mcp:srv", core.Allow); got != core.AskUser {
		t.Errorf("DefaultPolicy mcp-origin tool = %v, want AskUser", got)
	}
}

func TestDefaultPolicyAsksForMutatingTools(t *testing.T) {
	p := DefaultPolicy()
	for _, tool := range []string{"bash", "write", "edit", "repl", "spawn_agent"} {
		if got := decideFor(t, p, tool, core.Allow); got != core.AskUser {
			t.Errorf("DefaultPolicy(%s) = %v, want AskUser", tool, got)
		}
	}
	// advisor is read-only and gate-mandated, so it must not require approval.
	for _, tool := range []string{"read", "grep", "glob", "list_skills", "load_skill", "todowrite", "advisor"} {
		if got := decideFor(t, p, tool, core.Allow); got != core.Allow {
			t.Errorf("DefaultPolicy(%s) = %v, want Allow", tool, got)
		}
	}
}
