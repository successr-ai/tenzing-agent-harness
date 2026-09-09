package main

import (
	"path/filepath"
	"testing"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
)

// Path-valued tenzing.yaml keys resolve relative to the config file, not the
// process cwd, so a global config can name files sitting beside it.
func TestResolveFilePaths(t *testing.T) {
	cfgDir := filepath.FromSlash("/etc/tenzing")
	cfgPath := filepath.Join(cfgDir, "tenzing.yaml")
	abs := filepath.FromSlash("/opt/SYSTEM.md")

	tests := []struct {
		name         string
		in           cfgfile.File
		wantSystem   string
		wantNexus    string
		wantCommands []string
	}{
		{
			name:       "relative system_file resolves against the config dir",
			in:         cfgfile.File{SystemFile: "SYSTEM.md"},
			wantSystem: filepath.Join(cfgDir, "SYSTEM.md"),
		},
		{
			name:       "dot-relative system_file resolves against the config dir",
			in:         cfgfile.File{SystemFile: filepath.FromSlash("./prompts/SYSTEM.md")},
			wantSystem: filepath.Join(cfgDir, "prompts", "SYSTEM.md"),
		},
		{
			name:       "absolute system_file is left alone",
			in:         cfgfile.File{SystemFile: abs},
			wantSystem: abs,
		},
		{
			name:      "relative nexus_config resolves against the config dir",
			in:        cfgfile.File{NexusConfig: "nexus.yaml"},
			wantNexus: filepath.Join(cfgDir, "nexus.yaml"),
		},
		{
			name:       "empty values stay empty",
			in:         cfgfile.File{},
			wantSystem: "",
			wantNexus:  "",
		},
		{
			name: "mcp command with a separator resolves, bare name stays on PATH",
			in: cfgfile.File{MCPServers: []cfgfile.MCPServer{
				{Name: "local", Command: filepath.FromSlash("./bin/server")},
				{Name: "onpath", Command: "npx"},
				{Name: "absolute", Command: filepath.FromSlash("/usr/bin/thing")},
			}},
			wantCommands: []string{
				filepath.Join(cfgDir, "bin", "server"),
				"npx",
				filepath.FromSlash("/usr/bin/thing"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFilePaths(tt.in, cfgPath)

			if got.SystemFile != tt.wantSystem {
				t.Errorf("SystemFile = %q, want %q", got.SystemFile, tt.wantSystem)
			}
			if got.NexusConfig != tt.wantNexus {
				t.Errorf("NexusConfig = %q, want %q", got.NexusConfig, tt.wantNexus)
			}
			for i, want := range tt.wantCommands {
				if got.MCPServers[i].Command != want {
					t.Errorf("MCPServers[%d].Command = %q, want %q", i, got.MCPServers[i].Command, want)
				}
			}

			// The input must not be mutated: callers still hold it.
			if tt.in.SystemFile != "" && tt.in.SystemFile == got.SystemFile && !filepath.IsAbs(tt.in.SystemFile) {
				t.Error("input File was mutated in place")
			}
		})
	}
}
