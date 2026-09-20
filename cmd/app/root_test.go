package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

func TestMergeEnv(t *testing.T) {
	tests := []struct {
		name      string
		cfg       cliConfig // flag-side values before merge
		env       Config    // values config.Load would have parsed (incl. struct-tag defaults)
		changed   string    // flag name reported as explicitly passed
		present   string    // env var name reported as actually set
		wantPort  int
		wantDebug bool
		wantNexus string
	}{
		// Port branch (SERVER_PORT).
		{
			name: "explicit flag beats present env", changed: "port", present: "SERVER_PORT",
			cfg: cliConfig{Port: 9999}, env: Config{ServerPort: 7777}, wantPort: 9999,
		},
		{
			name: "env present wins over flag default", present: "SERVER_PORT",
			cfg: cliConfig{Port: 8080}, env: Config{ServerPort: 7777}, wantPort: 7777,
		},
		// Env var absent — config.Load's struct-tag default (8080) must NOT
		// override the flag default just because env.ServerPort != 0.
		{
			name: "env absent leaves flag default",
			cfg:  cliConfig{Port: 8080}, env: Config{ServerPort: 8080}, wantPort: 8080,
		},
		// Debug branch (LOG_DEBUG).
		{
			name: "explicit debug flag beats present env", changed: "debug", present: "LOG_DEBUG",
			cfg: cliConfig{Debug: true}, env: Config{LogDebug: false}, wantDebug: true,
		},
		{
			name: "LOG_DEBUG present wins over flag default", present: "LOG_DEBUG",
			cfg: cliConfig{Debug: false}, env: Config{LogDebug: true}, wantDebug: true,
		},
		{
			name: "LOG_DEBUG absent leaves flag default",
			cfg:  cliConfig{Debug: false}, env: Config{LogDebug: true}, wantDebug: false,
		},
		// Nexus-config branch (NEXUS_CONFIG).
		{
			name: "explicit nexus-config flag beats present env", changed: "nexus-config", present: "NEXUS_CONFIG",
			cfg: cliConfig{NexusConfig: "flag.yaml"}, env: Config{NexusConfig: "env.yaml"}, wantNexus: "flag.yaml",
		},
		{
			name: "NEXUS_CONFIG present wins over flag default", present: "NEXUS_CONFIG",
			cfg: cliConfig{NexusConfig: "nexus.yaml"}, env: Config{NexusConfig: "env.yaml"}, wantNexus: "env.yaml",
		},
		{
			name: "NEXUS_CONFIG absent leaves flag default",
			cfg:  cliConfig{NexusConfig: "nexus.yaml"}, env: Config{NexusConfig: "other.yaml"}, wantNexus: "nexus.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			mergeEnv(&cfg, &tt.env,
				func(name string) bool { return name == tt.changed },
				func(name string) bool { return name == tt.present },
			)
			if cfg.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", cfg.Port, tt.wantPort)
			}
			if cfg.Debug != tt.wantDebug {
				t.Errorf("Debug = %v, want %v", cfg.Debug, tt.wantDebug)
			}
			if cfg.NexusConfig != tt.wantNexus {
				t.Errorf("NexusConfig = %q, want %q", cfg.NexusConfig, tt.wantNexus)
			}
		})
	}
}

func TestMarkSetFlags(t *testing.T) {
	tests := []struct {
		name                string
		subagentDepthSet    bool
		approvalTimeoutSet  bool
		wantSubagentDepth   bool
		wantApprovalTimeout bool
	}{
		{"both changed", true, true, true, true},
		{"only subagent-depth changed", true, false, true, false},
		{"only approval-timeout changed", false, true, false, true},
		{"neither changed", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &cliConfig{}
			markSetFlags(cfg, func(name string) bool {
				switch name {
				case "subagent-depth":
					return tt.subagentDepthSet
				case "approval-timeout":
					return tt.approvalTimeoutSet
				default:
					return false
				}
			})
			if cfg.SubagentDepthSet != tt.wantSubagentDepth {
				t.Errorf("SubagentDepthSet = %v, want %v", cfg.SubagentDepthSet, tt.wantSubagentDepth)
			}
			if cfg.ApprovalTimeoutSet != tt.wantApprovalTimeout {
				t.Errorf("ApprovalTimeoutSet = %v, want %v", cfg.ApprovalTimeoutSet, tt.wantApprovalTimeout)
			}
		})
	}
}

// TestRootCmdWiresSetFlags proves RunE actually calls markSetFlags before
// dispatch (not just that the helper works in isolation): it swaps
// runPrintFn for a fake that captures the *cliConfig print mode receives,
// drives the real command line through Execute(), and inspects the
// captured config. Deleting the markSetFlags call site in RunE would fail
// this test.
func TestRootCmdWiresSetFlags(t *testing.T) {
	tests := []struct {
		name                   string
		args                   []string
		wantSubagentDepthSet   bool
		wantApprovalTimeoutSet bool
	}{
		{"both flags passed", []string{"-p", "hi", "--subagent-depth", "2", "--approval-timeout", "30s"}, true, true},
		{"neither flag passed", []string{"-p", "hi"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t, "")
			orig := runPrintFn
			t.Cleanup(func() { runPrintFn = orig })

			var got *cliConfig
			runPrintFn = func(_ context.Context, cfg *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
				got = cfg
				return nil
			}

			cmd := newRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			if got == nil {
				t.Fatal("runPrintFn was never called")
			}
			if got.SubagentDepthSet != tt.wantSubagentDepthSet {
				t.Errorf("SubagentDepthSet = %v, want %v", got.SubagentDepthSet, tt.wantSubagentDepthSet)
			}
			if got.ApprovalTimeoutSet != tt.wantApprovalTimeoutSet {
				t.Errorf("ApprovalTimeoutSet = %v, want %v", got.ApprovalTimeoutSet, tt.wantApprovalTimeoutSet)
			}
		})
	}
}

// TestPrintModeWarnsServeOnlyFlags proves print mode warns on stderr when
// serve-only flags are passed, and stays quiet when they aren't.
func TestPrintModeWarnsServeOnlyFlags(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantWarnings []string
	}{
		{
			"both serve-only flags warn",
			[]string{"-p", "hi", "--port", "9999", "--nexus-config", "x.yaml"},
			[]string{"--port is ignored in print mode", "--nexus-config is ignored in print mode"},
		},
		{"no serve-only flags, no warning", []string{"-p", "hi"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t, "")
			orig := runPrintFn
			t.Cleanup(func() { runPrintFn = orig })
			runPrintFn = func(_ context.Context, _ *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
				return nil
			}

			cmd := newRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			var errBuf bytes.Buffer
			cmd.SetErr(&errBuf)
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			if len(tt.wantWarnings) == 0 && errBuf.Len() != 0 {
				t.Errorf("expected no stderr output, got:\n%s", errBuf.String())
			}
			for _, w := range tt.wantWarnings {
				if !strings.Contains(errBuf.String(), w) {
					t.Errorf("stderr missing %q, got:\n%s", w, errBuf.String())
				}
			}
		})
	}
}

func TestRootCmdListModels(t *testing.T) {
	isolateConfig(t, "")
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--list-models"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--list-models: %v", err)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "beta") {
		t.Errorf("expected the declared models, got:\n%s", out.String())
	}
}

// TestRootCmdProviderFlag proves --provider reaches the registry: it
// repoints a provider the file already declares, and the model bound to
// that provider picks up the new endpoint.
func TestRootCmdProviderFlag(t *testing.T) {
	isolateConfig(t, "")

	orig := runPrintFn
	t.Cleanup(func() { runPrintFn = orig })
	var got *deps
	runPrintFn = func(_ context.Context, cfg *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
		got = cfg.deps
		return nil
	}

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"-p", "hi", "--provider",
		`{"name":"local","type":"ollama","url":"http://box:11434","api_key":"sk-flag"}`})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	rm, err := got.models.Resolve("alpha")
	if err != nil {
		t.Fatalf("resolve alpha: %v", err)
	}
	if rm.Provider.URL != "http://box:11434" || rm.Provider.APIKey != "sk-flag" {
		t.Errorf("--provider did not reach the registry: %+v", rm.Provider)
	}
}

func TestRootCmdRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"--bogus"}},
		{"bad output format", []string{"-p", "hi", "--output-format", "yaml"}},
		{"bad model", []string{"-p", "hi", "--model", "nope/nope"}},
		{"output format without -p", []string{"--output-format", "json"}},
		{"explicit empty prompt", []string{"-p", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

// testConfigYAML is a minimal complete config: one provider, one model.
// Every root-command test needs one now — there is no compiled-in model set
// to fall back on.
const testConfigYAML = "providers:\n" +
	"  - name: local\n    type: ollama\n    url: http://localhost:11434\n" +
	"models:\n" +
	"  - name: alpha\n    provider: local\n    model_name: glm-5.3\n" +
	"  - name: beta\n    provider: local\n    model_name: qwen3\n"

// isolateConfig points TENZING_CONFIG at a minimal complete config and
// redirects the per-user fallback, so a root-command test never reads (or
// is rescued by) the developer's own tenzing.yaml. Returns the path.
func isolateConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenzing.yaml")
	if err := os.WriteFile(path, []byte(testConfigYAML+"model: alpha\n"+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENZING_CONFIG", path)
	redirectUserConfig(t)
	return path
}

// TestRootCmdModelPrecedence proves the effective model order:
// --model flag > tenzing.yaml model:. There is no third source.
func TestRootCmdModelPrecedence(t *testing.T) {
	write := func(content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "tenzing.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	withModel := write(testConfigYAML + "model: beta\n")
	noModel := write(testConfigYAML)

	tests := []struct {
		name      string
		args      []string
		config    string // TENZING_CONFIG; "" = absent file
		wantModel string
		wantErr   string
	}{
		{name: "flag beats file", args: []string{"-p", "hi", "--model", "alpha"}, config: withModel, wantModel: "alpha"},
		{name: "file model applies", args: []string{"-p", "hi"}, config: withModel, wantModel: "beta"},
		{name: "no model anywhere errors", args: []string{"-p", "hi"}, config: noModel, wantErr: "no model selected"},
		{name: "no config file errors", args: []string{"-p", "hi"}, wantErr: "tenzing init"},
		{name: "undeclared model errors", args: []string{"-p", "hi", "--model", "nope"}, config: withModel, wantErr: "not declared in models"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TENZING_CONFIG", tt.config)
			// Isolate the per-user config fallback: without this the
			// developer's own user config would satisfy the "no file" case.
			redirectUserConfig(t)

			orig := runPrintFn
			t.Cleanup(func() { runPrintFn = orig })
			var got *cliConfig
			runPrintFn = func(_ context.Context, cfg *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
				got = cfg
				return nil
			}

			cmd := newRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got == nil {
				t.Fatal("runPrintFn was never called")
			}
			if got.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantModel)
			}
		})
	}
}

// TestMergeConfigFile proves the file layer sits under flags and env:
// values apply only when neither the flag nor the env var spoke.
func TestMergeConfigFile(t *testing.T) {
	changedNone := func(string) bool { return false }
	presentNone := func(string) bool { return false }
	dur := func(d time.Duration) *cfgfile.Duration { c := cfgfile.Duration(d); return &c }
	intp := func(i int) *int { return &i }
	boolp := func(b bool) *bool { return &b }
	int64p := func(i int64) *int64 { return &i }

	t.Run("file fills unset fields", func(t *testing.T) {
		cfg := &cliConfig{Port: 8080, NexusConfig: "nexus.yaml"}
		mergeConfigFile(cfg, cfgfile.File{
			SubagentModel:   "sub",
			AdvisorModel:    "adv",
			AdvisorNudge:    3,
			AdvisorCadence:  -1,
			AdvisorMaxCalls: 12,
			MaxTurnTokens:   500,
			MaxIterations:   9,
			MaxWallClock:    dur(10 * time.Minute),
			ThinkingBudget:  int64p(8192),
			SubagentDepth:   intp(0),
			ApprovalTimeout: dur(90 * time.Second),
			Thinking:        boolp(true),
			ReadOnly:        true,
			SystemFile:      "sys.md",
			Port:            intp(9090),
			NexusConfig:     "nx.yaml",
			Debug:           true,
			MCPServers:      []cfgfile.MCPServer{{Name: "fs", Command: "npx", Args: []string{"-y"}}},
		}, changedNone, presentNone)

		if cfg.SubagentModel != "sub" || cfg.AdvisorModel != "adv" || cfg.AdvisorNudge != 3 {
			t.Errorf("model fields not merged: %+v", cfg)
		}
		if cfg.AdvisorCadence != -1 || cfg.AdvisorMaxCalls != 12 {
			t.Errorf("advisor cadence/max-calls not merged: %+v", cfg)
		}
		if cfg.MaxTurnTokens != 500 || cfg.MaxIterations != 9 || cfg.MaxWallClock != 10*time.Minute {
			t.Errorf("budget fields not merged: %+v", cfg)
		}
		if cfg.ThinkingBudget != 8192 {
			t.Errorf("thinking_budget not merged: %+v", cfg)
		}
		if cfg.SubagentDepth != 0 || !cfg.SubagentDepthSet {
			t.Errorf("subagent_depth: got %d set=%v, want explicit 0", cfg.SubagentDepth, cfg.SubagentDepthSet)
		}
		if cfg.ApprovalTimeout != 90*time.Second || !cfg.ApprovalTimeoutSet {
			t.Errorf("approval_timeout not merged with Set marker: %+v", cfg)
		}
		if !cfg.Thinking || !cfg.ThinkingSet {
			t.Errorf("thinking not merged with Set marker: %+v", cfg)
		}
		if !cfg.ReadOnly || cfg.SystemFile != "sys.md" {
			t.Errorf("toggle/path fields not merged: %+v", cfg)
		}
		if cfg.Port != 9090 || cfg.NexusConfig != "nx.yaml" || !cfg.Debug {
			t.Errorf("serve fields not merged: %+v", cfg)
		}
		if len(cfg.MCPServerConfigs) != 1 || cfg.MCPServerConfigs[0].Name != "fs" {
			t.Errorf("mcp servers not merged: %+v", cfg.MCPServerConfigs)
		}
	})

	t.Run("changed flag beats file", func(t *testing.T) {
		cfg := &cliConfig{AdvisorModel: "ollama/flag", Port: 7777, ThinkingBudget: 2048}
		mergeConfigFile(cfg, cfgfile.File{AdvisorModel: "ollama/file", Port: intp(9090), ThinkingBudget: int64p(8192)},
			func(name string) bool { return name == "advisor-model" || name == "port" || name == "thinking-budget" },
			presentNone)
		if cfg.AdvisorModel != "ollama/flag" {
			t.Errorf("flag lost to file: %q", cfg.AdvisorModel)
		}
		if cfg.ThinkingBudget != 2048 {
			t.Errorf("thinking-budget flag lost to file: %d", cfg.ThinkingBudget)
		}
		if cfg.Port != 7777 {
			t.Errorf("port flag lost to file: %d", cfg.Port)
		}
	})

	t.Run("present env beats file for env-backed settings", func(t *testing.T) {
		cfg := &cliConfig{Port: 8081, NexusConfig: "env.yaml", Debug: true}
		mergeConfigFile(cfg, cfgfile.File{Port: intp(9090), NexusConfig: "nx.yaml", Debug: false},
			changedNone,
			func(name string) bool { return true })
		if cfg.Port != 8081 || cfg.NexusConfig != "env.yaml" || !cfg.Debug {
			t.Errorf("env lost to file: %+v", cfg)
		}
	})

	t.Run("omitted file keys leave defaults", func(t *testing.T) {
		cfg := &cliConfig{Port: 8080, SubagentDepth: 1}
		mergeConfigFile(cfg, cfgfile.File{}, changedNone, presentNone)
		if cfg.Port != 8080 || cfg.SubagentDepth != 1 || cfg.SubagentDepthSet || cfg.ThinkingSet {
			t.Errorf("zero file changed defaults: %+v", cfg)
		}
	})
}

// TestRootCmdConfigFile drives --config end-to-end through RunE.
func TestRootCmdConfigFile(t *testing.T) {
	t.Run("full file drives print-mode config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tenzing.yaml")
		content := testConfigYAML + "model: beta\nmax_iterations: 7\nsubagent_depth: 0\nread_only: true\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		orig := runPrintFn
		t.Cleanup(func() { runPrintFn = orig })
		var got *cliConfig
		runPrintFn = func(_ context.Context, cfg *cliConfig, _, _ io.Writer, _ ...harness.HarnessOption) error {
			got = cfg
			return nil
		}

		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"-p", "hi", "--config", path})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got == nil {
			t.Fatal("runPrintFn was never called")
		}
		if got.Model != "beta" {
			t.Errorf("Model = %q, want beta", got.Model)
		}
		if got.MaxIterations != 7 || !got.ReadOnly {
			t.Errorf("file settings not applied: %+v", got)
		}
		if got.SubagentDepth != 0 || !got.SubagentDepthSet {
			t.Errorf("subagent_depth 0 not applied: depth=%d set=%v", got.SubagentDepth, got.SubagentDepthSet)
		}
		if _, err := got.deps.models.Resolve("alpha"); err != nil {
			t.Errorf("model from config not registered: %v", err)
		}
	})

	t.Run("explicit missing file errors", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"-p", "hi", "--config", filepath.Join(t.TempDir(), "nope.yaml")})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "nope.yaml") {
			t.Fatalf("err = %v, want missing-file error naming the path", err)
		}
	})

	t.Run("list-models includes config entries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tenzing.yaml")
		if err := os.WriteFile(path, []byte(testConfigYAML), 0o644); err != nil {
			t.Fatal(err)
		}

		out := &bytes.Buffer{}
		cmd := newRootCmd()
		cmd.SetOut(out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--list-models", "--config", path})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "glm-5.3") {
			t.Errorf("--list-models missing config entry:\n%s", out.String())
		}
	})
}

// The nested models: shape reaches the registry: --list-models shows both
// kinds, grouped, with the System One model's wire id. The rest of the
// root-command tests use the pre-split sequence shape, which is the other
// half of this proof — both must keep working.
func TestRootCmdListsSystemOneModels(t *testing.T) {
	const yaml = "providers:\n" +
		"  - name: local\n    type: ollama\n    url: http://localhost:11434\n" +
		"  - name: openrouter-jev\n    type: systemone\n    url: https://openrouter.ai/api\n" +
		"models:\n" +
		"  llm:\n    - name: alpha\n      provider: local\n      model_name: glm-5.3\n" +
		"  systemone:\n    - name: jev\n      provider: openrouter-jev\n      model_name: typesafe/jev-1.13\n" +
		"model: alpha\n"

	path := filepath.Join(t.TempDir(), "tenzing.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectUserConfig(t)

	out := &bytes.Buffer{}
	cmd := newRootCmd()
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--list-models", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"llm:", "alpha", "systemone:", "jev", "typesafe/jev-1.13"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--list-models missing %q:\n%s", want, out.String())
		}
	}
}
