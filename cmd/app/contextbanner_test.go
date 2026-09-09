package main

import (
	"strings"
	"testing"
)

func TestPrintContextFiles(t *testing.T) {
	tests := []struct {
		name      string
		paths     []string
		disabled  bool
		truncated bool
		want      string
	}{
		{
			name:  "none loaded",
			paths: nil,
			want:  "  context 0 loaded\n",
		},
		{
			name:      "truncation is flagged, not hidden",
			paths:     []string{"/repo/AGENTS.md"},
			truncated: true,
			want:      "  context 1 loaded (truncated — tail dropped)\n    - /repo/AGENTS.md\n",
		},
		{
			name:     "disabled says so instead of counting",
			disabled: true,
			want:     "  context disabled (--no-context-files)\n",
		},
		{
			name:     "disabled wins over any paths",
			paths:    []string{"/repo/AGENTS.md"},
			disabled: true,
			want:     "  context disabled (--no-context-files)\n",
		},
		{
			name:  "append order preserved, not sorted",
			paths: []string{"/cfg/tenzing/AGENTS.md", "/repo/AGENTS.md", "/repo/a/AGENTS.md"},
			want:  "  context 3 loaded\n    - /cfg/tenzing/AGENTS.md\n    - /repo/AGENTS.md\n    - /repo/a/AGENTS.md\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			printContextFiles(&b, tt.paths, tt.disabled, tt.truncated)
			if got := b.String(); got != tt.want {
				t.Errorf("printContextFiles() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestPrintRuleFiles(t *testing.T) {
	tests := []struct {
		name     string
		paths    []string
		disabled bool
		want     string
	}{
		{
			name:  "none loaded",
			paths: nil,
			want:  "  rules   0 loaded\n",
		},
		{
			name:  "glob order preserved",
			paths: []string{"/r/agents.md", "/r/coding-style.md"},
			want:  "  rules   2 loaded\n    - /r/agents.md\n    - /r/coding-style.md\n",
		},
		{
			name:     "disabled prints nothing, the context line says why",
			paths:    []string{"/r/agents.md"},
			disabled: true,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			printRuleFiles(&b, tt.paths, tt.disabled)
			if got := b.String(); got != tt.want {
				t.Errorf("printRuleFiles() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
