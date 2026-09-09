package main

import (
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// permissionPolicy applies a tenzing.yaml `permissions:` section on top of
// permissions.DefaultPolicy. A tool named in one list is removed from the
// other two — otherwise the policy's Deny > Ask > Allow precedence would let
// a default Ask (write, edit) outrank a configured Allow.
func permissionPolicy(s config.PermissionsSection) permissions.Policy {
	p := permissions.DefaultPolicy()
	p.Allow = merge(p.Allow, s.Allow, s.Deny, s.Ask)
	p.Deny = merge(p.Deny, s.Deny, s.Allow, s.Ask)
	p.Ask = merge(p.Ask, s.Ask, s.Allow, s.Deny)
	if len(s.AskOrigins) > 0 {
		p.AskOrigins = s.AskOrigins
	}
	return p
}

// merge appends add to base, dropping any name claimed by another list.
func merge(base, add []string, others ...[]string) []string {
	claimed := map[string]bool{}
	for _, o := range others {
		for _, n := range o {
			claimed[strings.ToLower(n)] = true
		}
	}
	out := make([]string, 0, len(base)+len(add))
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, base...), add...) {
		k := strings.ToLower(n)
		if claimed[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}
