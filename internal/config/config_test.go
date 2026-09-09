package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenzing.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_FullFile(t *testing.T) {
	path := writeFile(t, `
model: openrouter/main
subagent_model: openrouter/sub
advisor_model: openrouter/adv
advisor_nudge: 3
max_tokens: 100000
max_iterations: 50
max_wall_clock: "10m"
subagent_depth: 0
approval_timeout: "90s"
no_permissions: true
dangerously_skip_permissions: true
read_only: true
thinking: false
no_session: true
no_context_files: true
system_file: sys.md
base_url: http://box:11434
api_key: sk-test
port: 9090
nexus_config: nx.yaml
debug: true
mcp_servers:
  - name: fs
    command: npx
    args: ["-y", "server-filesystem"]
models:
  default: openrouter/custom
  entries:
    - provider: openrouter
      name: custom
      context_window: 200000
      max_tokens: 16384
      vision: true
      cost: {input: 1.5, output: 6}
`)
	f, found, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("found = false")
	}

	if f.Model != "openrouter/main" || f.SubagentModel != "openrouter/sub" || f.AdvisorModel != "openrouter/adv" {
		t.Errorf("model refs wrong: %+v", f)
	}
	if f.AdvisorNudge != 3 || f.MaxTokens != 100000 || f.MaxIterations != 50 {
		t.Errorf("numeric fields wrong: %+v", f)
	}
	if f.MaxWallClock.Value() != 10*time.Minute {
		t.Errorf("max_wall_clock = %v, want 10m", f.MaxWallClock.Value())
	}
	if f.SubagentDepth == nil || *f.SubagentDepth != 0 {
		t.Errorf("subagent_depth: want explicit 0, got %v", f.SubagentDepth)
	}
	if f.ApprovalTimeout.Value() != 90*time.Second {
		t.Errorf("approval_timeout = %v, want 90s", f.ApprovalTimeout.Value())
	}
	if f.Thinking == nil || *f.Thinking != false {
		t.Errorf("thinking: want explicit false, got %v", f.Thinking)
	}
	if !f.NoPermissions || !f.SkipPermissions || !f.ReadOnly || !f.NoSession || !f.NoContextFiles || !f.Debug {
		t.Errorf("bool toggles wrong: %+v", f)
	}
	if f.Port == nil || *f.Port != 9090 || f.NexusConfig != "nx.yaml" {
		t.Errorf("serve fields wrong: %+v", f)
	}
	if f.SystemFile != "sys.md" || f.BaseURL != "http://box:11434" || f.APIKey != "sk-test" {
		t.Errorf("path/url fields wrong: %+v", f)
	}
	if len(f.MCPServers) != 1 || f.MCPServers[0].Name != "fs" || f.MCPServers[0].Command != "npx" || len(f.MCPServers[0].Args) != 2 {
		t.Errorf("mcp_servers wrong: %+v", f.MCPServers)
	}
	if f.Models.Default != "openrouter/custom" || len(f.Models.Entries) != 1 {
		t.Fatalf("models section wrong: %+v", f.Models)
	}
	e := f.Models.Entries[0]
	if e.Provider != "openrouter" || e.Name != "custom" || e.ContextWindow != 200000 || e.MaxTokens != 16384 || !e.Vision {
		t.Errorf("model entry wrong: %+v", e)
	}
	if e.Cost == nil || e.Cost.Input != 1.5 || e.Cost.Output != 6 {
		t.Errorf("cost wrong: %+v", e.Cost)
	}
}

func TestLoad_OmittedPointersStayNil(t *testing.T) {
	f, found, err := Load(writeFile(t, "model: openrouter/x\n"), true)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if f.SubagentDepth != nil || f.ApprovalTimeout != nil || f.Thinking != nil || f.Port != nil || f.MaxWallClock != nil {
		t.Errorf("omitted pointer fields not nil: %+v", f)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"unknown key", "advisor_modle: x\n", "advisor_modle"},
		{"bad duration", "max_wall_clock: \"fast\"\n", "invalid duration"},
		{"bad duration type", "approval_timeout: [1]\n", "duration"},
		{"stale models.yaml", "default: a/b\nmodels:\n  - provider: p\n    name: n\n", "models.yaml"},
		{"mcp server missing command", "mcp_servers:\n  - name: fs\n", "name and command are required"},
		{"model entry missing name", "models:\n  entries:\n    - provider: openrouter\n", "provider and name are required"},
		{"negative nudge", "advisor_nudge: -1\n", "advisor_nudge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(writeFile(t, tt.content), true)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")

	if _, _, err := Load(missing, true); err == nil {
		t.Error("explicit missing file: want error, got nil")
	}

	f, found, err := Load(missing, false)
	if err != nil {
		t.Errorf("default-path missing file: err = %v, want nil", err)
	}
	if found {
		t.Error("default-path missing file: found = true, want false")
	}
	if f.Model != "" {
		t.Errorf("zero File expected, got %+v", f)
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	f, found, err := Load(writeFile(t, ""), true)
	if err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if !found {
		t.Error("empty file: found = false, want true")
	}
	if f.Model != "" || f.SubagentDepth != nil {
		t.Errorf("empty file should decode to zero File, got %+v", f)
	}
}

func TestLoad_ExpandsEnv(t *testing.T) {
	t.Setenv("TENZING_TEST_KEY", "sk-secret")

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"bare", "api_key: $TENZING_TEST_KEY", "sk-secret"},
		{"braced", `api_key: "${TENZING_TEST_KEY}"`, "sk-secret"},
		{"unset left alone", "api_key: $TENZING_TEST_MISSING", "$TENZING_TEST_MISSING"},
		{"no reference", "api_key: plain", "plain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, err := Load(writeFile(t, tt.yaml), true)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if f.APIKey != tt.want {
				t.Errorf("APIKey = %q, want %q", f.APIKey, tt.want)
			}
		})
	}
}
