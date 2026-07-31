package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	pkgmodels "github.com/successr-ai/tenzing-agent-harness/pkg/models"
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
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--list-models"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--list-models: %v", err)
	}
	if !strings.Contains(out.String(), "anthropic/") {
		t.Errorf("expected model list, got:\n%s", out.String())
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

// TestRootCmdModelPrecedence proves the effective model order:
// --model flag > TENZING_MODEL env > models.yaml default > compiled default.
func TestRootCmdModelPrecedence(t *testing.T) {
	glm := modelKey(pkgmodels.Ollama_GLM5_2_Cloud.(common.ModelDefinition).Provider, pkgmodels.Ollama_GLM5_2_Cloud.GetName())
	qwen := modelKey(pkgmodels.Ollama_Qwen3_5_9B.(common.ModelDefinition).Provider, pkgmodels.Ollama_Qwen3_5_9B.GetName())
	qwenBig := modelKey(pkgmodels.Ollama_Qwen3_5_35B.(common.ModelDefinition).Provider, pkgmodels.Ollama_Qwen3_5_35B.GetName())

	yamlPath := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(yamlPath, []byte("default: "+qwenBig+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		args      []string
		env       string // TENZING_MODEL; "" = unset
		models    string // TENZING_MODELS_CONFIG; "" = absent file
		wantModel string
	}{
		{"flag beats env and yaml", []string{"-p", "hi", "--model", glm}, qwen, yamlPath, glm},
		{"env beats yaml default", []string{"-p", "hi"}, qwen, yamlPath, qwen},
		{"yaml default beats compiled", []string{"-p", "hi"}, "", yamlPath, qwenBig},
		{"compiled default", []string{"-p", "hi"}, "", "", glm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TENZING_MODEL", tt.env)
			modelsPath := tt.models
			if modelsPath == "" {
				modelsPath = filepath.Join(t.TempDir(), "absent.yaml")
			}
			t.Setenv("TENZING_MODELS_CONFIG", modelsPath)
			t.Cleanup(func() { models = emptyRegistry() })

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
			if got.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantModel)
			}
		})
	}
}
