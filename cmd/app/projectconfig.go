package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/prompts"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

// projectConfig holds harness options derived from drop-in config files
// (F24 system-prompt overrides, F22 prompt-template dirs), plus what was
// decided, for startup logging and tests.
type projectConfig struct {
	sysOpt       harness.HarnessOption // nil when no override file applies
	tmplOpts     []harness.HarnessOption
	systemFile   string // override file applied; "" = default prompt
	appended     bool   // systemFile was an APPEND_SYSTEM.md
	templateDirs []string
	skipped      []string // project-local sources ignored: directory untrusted
}

// loadProjectConfig probes system-prompt override files and prompt-template
// directories. SYSTEM.md replaces the prompt, APPEND_SYSTEM.md appends to
// the rendered default; project (cwd) beats global (<cfgDir>/tenzing/),
// replace beats append. Project-local sources apply only when trusted (F26);
// global ones always.
func loadProjectConfig(cwd, cfgDir string, trusted bool) projectConfig {
	pc := projectConfig{}

	type candidate struct {
		path    string
		project bool
	}
	candidates := func(name string) []candidate {
		out := []candidate{{filepath.Join(cwd, name), true}}
		if cfgDir != "" {
			out = append(out, candidate{filepath.Join(cfgDir, "tenzing", name), false})
		}
		return out
	}
	probe := func(cands []candidate) string {
		for _, c := range cands {
			if _, err := os.Stat(c.path); err != nil {
				continue
			}
			if c.project && !trusted {
				pc.skipped = append(pc.skipped, c.path)
				continue
			}
			return c.path
		}
		return ""
	}
	read := func(path string) (string, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("system prompt override unreadable, using default", "path", path, "error", err)
			return "", false
		}
		return strings.TrimSpace(string(data)), true
	}

	if p := probe(candidates("SYSTEM.md")); p != "" {
		if content, ok := read(p); ok {
			pc.sysOpt = harness.WithSystemPrompt(content)
			pc.systemFile = p
		}
	} else if p := probe(candidates("APPEND_SYSTEM.md")); p != "" {
		if content, ok := read(p); ok {
			pc.sysOpt = harness.WithSystemPrompt(prompts.DefaultSystemPrompt() + "\n\n" + content)
			pc.systemFile = p
			pc.appended = true
		}
	}

	// Prompt-template dirs: global first, project second so project names
	// win on collision inside the registry.
	if cfgDir != "" {
		pc.templateDirs = append(pc.templateDirs, filepath.Join(cfgDir, "tenzing", "prompts"))
	}
	projTmpl := filepath.Join(cwd, ".tenzing", "prompts")
	if trusted {
		pc.templateDirs = append(pc.templateDirs, projTmpl)
	} else if _, err := os.Stat(projTmpl); err == nil {
		pc.skipped = append(pc.skipped, projTmpl)
	}
	for _, d := range pc.templateDirs {
		pc.tmplOpts = append(pc.tmplOpts, harness.WithPromptTemplatesDir(d))
	}

	return pc
}

// harnessOpts returns every option the project config produced.
func (pc projectConfig) harnessOpts() []harness.HarnessOption {
	opts := append([]harness.HarnessOption{}, pc.tmplOpts...)
	if pc.sysOpt != nil {
		opts = append(opts, pc.sysOpt)
	}
	return opts
}

// logDecisions reports what was applied and what was skipped at startup.
func (pc projectConfig) logDecisions() {
	for _, p := range pc.skipped {
		slog.Info("project config skipped: directory untrusted (grant via POST /trust, --trust, or TENZING_PROJECT_TRUST=trust)", "path", p)
	}
	switch {
	case pc.systemFile == "":
		slog.Info("system prompt: default")
	case pc.appended:
		slog.Info("system prompt: default + append file", "path", pc.systemFile)
	default:
		slog.Info("system prompt: replaced by override file", "path", pc.systemFile)
	}
	slog.Info("prompt template dirs", "dirs", pc.templateDirs)
}
