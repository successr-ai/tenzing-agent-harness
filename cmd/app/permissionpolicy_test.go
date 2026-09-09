package main

import (
	"slices"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
)

func TestPermissionPolicy(t *testing.T) {
	tests := []struct {
		name        string
		section     config.PermissionsSection
		wantAllow   []string // must be present in Allow
		wantNotAsk  []string // must be absent from Ask
		wantAsk     []string // must be present in Ask
		wantOrigins []string
	}{
		{
			name:        "empty section keeps defaults",
			wantAsk:     []string{"bash", "write", "edit"},
			wantOrigins: []string{"mcp:"},
		},
		{
			name:        "allow promotes tools out of the default ask list",
			section:     config.PermissionsSection{Allow: []string{"Read", "Write", "Edit"}},
			wantAllow:   []string{"Read", "Write", "Edit"},
			wantNotAsk:  []string{"write", "edit"},
			wantAsk:     []string{"bash", "repl", "spawn_agent"},
			wantOrigins: []string{"mcp:"},
		},
		{
			name:        "deny wins over the default ask list",
			section:     config.PermissionsSection{Deny: []string{"bash"}},
			wantNotAsk:  []string{"bash"},
			wantOrigins: []string{"mcp:"},
		},
		{
			name:        "ask_origins replaces the default prefixes",
			section:     config.PermissionsSection{AskOrigins: []string{"remote:"}},
			wantOrigins: []string{"remote:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := permissionPolicy(tt.section)
			for _, n := range tt.wantAllow {
				if !slices.Contains(p.Allow, n) {
					t.Errorf("Allow = %v, want it to contain %q", p.Allow, n)
				}
			}
			for _, n := range tt.wantAsk {
				if !slices.Contains(p.Ask, n) {
					t.Errorf("Ask = %v, want it to contain %q", p.Ask, n)
				}
			}
			for _, n := range tt.wantNotAsk {
				if slices.Contains(p.Ask, n) {
					t.Errorf("Ask = %v, want it to omit %q", p.Ask, n)
				}
			}
			if !slices.Equal(p.AskOrigins, tt.wantOrigins) {
				t.Errorf("AskOrigins = %v, want %v", p.AskOrigins, tt.wantOrigins)
			}
			if tt.section.Deny != nil && !slices.Equal(p.Deny, tt.section.Deny) {
				t.Errorf("Deny = %v, want %v", p.Deny, tt.section.Deny)
			}
		})
	}
}
