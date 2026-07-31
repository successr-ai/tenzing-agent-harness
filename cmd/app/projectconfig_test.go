package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/prompts"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

func seedProjectFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProjectConfig(t *testing.T) {
	tests := []struct {
		name        string
		seed        func(t *testing.T, cwd, cfgDir string)
		trusted     bool
		wantFile    func(cwd, cfgDir string) string // "" = default prompt
		wantAppend  bool
		wantSkipped int
	}{
		{
			name:     "no files default prompt",
			seed:     func(t *testing.T, cwd, cfgDir string) {},
			trusted:  true,
			wantFile: func(cwd, cfgDir string) string { return "" },
		},
		{
			name: "project SYSTEM.md replaces when trusted",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, cwd, "SYSTEM.md", "project prompt")
			},
			trusted:  true,
			wantFile: func(cwd, cfgDir string) string { return filepath.Join(cwd, "SYSTEM.md") },
		},
		{
			name: "project SYSTEM.md skipped when untrusted, global honored",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, cwd, "SYSTEM.md", "project prompt")
				seedProjectFile(t, filepath.Join(cfgDir, "tenzing"), "SYSTEM.md", "global prompt")
			},
			trusted:     false,
			wantFile:    func(cwd, cfgDir string) string { return filepath.Join(cfgDir, "tenzing", "SYSTEM.md") },
			wantSkipped: 1,
		},
		{
			name: "untrusted with only project file falls back to default",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, cwd, "SYSTEM.md", "project prompt")
			},
			trusted:     false,
			wantFile:    func(cwd, cfgDir string) string { return "" },
			wantSkipped: 1,
		},
		{
			name: "project beats global",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, cwd, "SYSTEM.md", "project prompt")
				seedProjectFile(t, filepath.Join(cfgDir, "tenzing"), "SYSTEM.md", "global prompt")
			},
			trusted:  true,
			wantFile: func(cwd, cfgDir string) string { return filepath.Join(cwd, "SYSTEM.md") },
		},
		{
			name: "replace beats append",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, filepath.Join(cfgDir, "tenzing"), "SYSTEM.md", "global prompt")
				seedProjectFile(t, cwd, "APPEND_SYSTEM.md", "extra")
			},
			trusted:  true,
			wantFile: func(cwd, cfgDir string) string { return filepath.Join(cfgDir, "tenzing", "SYSTEM.md") },
		},
		{
			name: "append file appends",
			seed: func(t *testing.T, cwd, cfgDir string) {
				seedProjectFile(t, cwd, "APPEND_SYSTEM.md", "marker instruction")
			},
			trusted:    true,
			wantFile:   func(cwd, cfgDir string) string { return filepath.Join(cwd, "APPEND_SYSTEM.md") },
			wantAppend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd, cfgDir := t.TempDir(), t.TempDir()
			tt.seed(t, cwd, cfgDir)

			pc := loadProjectConfig(cwd, cfgDir, tt.trusted)

			if want := tt.wantFile(cwd, cfgDir); pc.systemFile != want {
				t.Errorf("systemFile = %q, want %q", pc.systemFile, want)
			}
			if pc.appended != tt.wantAppend {
				t.Errorf("appended = %v, want %v", pc.appended, tt.wantAppend)
			}
			if len(pc.skipped) != tt.wantSkipped {
				t.Errorf("skipped = %#v, want %d entries", pc.skipped, tt.wantSkipped)
			}
			if (pc.sysOpt == nil) != (pc.systemFile == "") {
				t.Errorf("sysOpt/systemFile disagree: opt nil=%v file=%q", pc.sysOpt == nil, pc.systemFile)
			}
		})
	}
}

// End-to-end through harness.New: SYSTEM.md replaces the prompt wholesale,
// APPEND_SYSTEM.md keeps the default and appends (F24 acceptance).
func TestProjectConfigSystemPromptContent(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		contains []string
		excludes []string
	}{
		{
			name:     "replace",
			file:     "SYSTEM.md",
			contains: []string{"MARKER INSTRUCTION"},
			excludes: []string{"spawn_agent"}, // default prompt gone
		},
		{
			name:     "append",
			file:     "APPEND_SYSTEM.md",
			contains: []string{prompts.DefaultSystemPrompt(), "MARKER INSTRUCTION"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd, cfgDir := t.TempDir(), t.TempDir()
			seedProjectFile(t, cwd, tt.file, "MARKER INSTRUCTION")
			pc := loadProjectConfig(cwd, cfgDir, true)

			var captured string
			opts := append(pc.harnessOpts(),
				harness.WithAgentBuilder(func(_ common.LLM, sp string) (core.Agent, error) {
					captured = sp
					return &gatedAgent{gate: make(chan struct{})}, nil
				}),
				harness.WithSubagentDepth(0),
				harness.WithContextFilesDisabled(),
				harness.WithSessionDir(t.TempDir()),
			)
			h, err := harness.New(&stubLLM{}, opts...)
			if err != nil {
				t.Fatalf("harness.New: %v", err)
			}
			t.Cleanup(h.Shutdown)

			for _, want := range tt.contains {
				if !strings.Contains(captured, want) {
					t.Errorf("prompt missing %q:\n%s", want, captured)
				}
			}
			for _, not := range tt.excludes {
				if strings.Contains(captured, not) {
					t.Errorf("prompt unexpectedly contains %q", not)
				}
			}
		})
	}
}

func TestLoadProjectConfigTemplateDirs(t *testing.T) {
	cwd, cfgDir := t.TempDir(), t.TempDir()
	projDir := filepath.Join(cwd, ".tenzing", "prompts")
	globalDir := filepath.Join(cfgDir, "tenzing", "prompts")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	trustedPC := loadProjectConfig(cwd, cfgDir, true)
	// global first, project second — project names win in the registry
	if !slices.Equal(trustedPC.templateDirs, []string{globalDir, projDir}) {
		t.Errorf("trusted templateDirs = %#v, want [%s %s]", trustedPC.templateDirs, globalDir, projDir)
	}

	untrustedPC := loadProjectConfig(cwd, cfgDir, false)
	if !slices.Equal(untrustedPC.templateDirs, []string{globalDir}) {
		t.Errorf("untrusted templateDirs = %#v, want [%s]", untrustedPC.templateDirs, globalDir)
	}
	if !slices.Contains(untrustedPC.skipped, projDir) {
		t.Errorf("expected %s in skipped, got %#v", projDir, untrustedPC.skipped)
	}
}
