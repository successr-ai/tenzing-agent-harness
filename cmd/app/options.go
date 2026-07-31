package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// cliConfig holds every flag value after cobra parsing and env merging.
// *Set fields record whether the flag was explicitly passed (cobra Changed) —
// needed where "unset" and "zero" mean different things.
type cliConfig struct {
	// mode
	Prompt       string
	OutputFormat string // "text" | "json"
	ListModels   bool

	// models
	Model           string
	SubagentModel   string
	BlackboardModel string
	AdvisorModel    string

	// budgets
	MaxTokens     int64
	MaxIterations int
	MaxWallClock  time.Duration

	// toggles
	SubagentDepth      int
	SubagentDepthSet   bool
	ApprovalTimeout    time.Duration
	ApprovalTimeoutSet bool
	NoPermissions      bool
	SkipPermissions    bool
	ReadOnly           bool
	Thinking           bool
	ThinkingSet        bool
	NoSession          bool
	NoContextFiles     bool

	// prompt / sessions / trust
	SystemFile     string // file whose contents replace the system prompt
	Resume         string // conversation ID to resume
	ContinueLatest bool   // resume the latest session for cwd
	Trust          bool   // one-shot: treat cwd as trusted for this run
	Timeout        time.Duration

	// wiring
	MCPServers     []string
	ConversationID string

	// serve
	Port        int
	NexusConfig string
	Debug       bool

	// env-only (no flags; filled from Config in RunE)
	ModelsConfig string
	ProjectTrust string

	// test seams for --continue resolution; "" means the real defaults
	// (session.DefaultDir() and os.Getwd()).
	sessionDir string
	cwd        string
}

// parseMCPServer parses "name=command arg1 arg2" into an mcp.ServerConfig.
func parseMCPServer(s string) (mcp.ServerConfig, error) {
	name, rest, found := strings.Cut(s, "=")
	if !found || name == "" {
		return mcp.ServerConfig{}, fmt.Errorf("mcp server %q: want format name=command [args...]", s)
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return mcp.ServerConfig{}, fmt.Errorf("mcp server %q: empty command", s)
	}
	return mcp.ServerConfig{Name: name, Command: fields[0], Args: fields[1:]}, nil
}

// harnessOptions maps flag values to harness options shared by serve and
// print modes. Mode-specific options (event bus, delta handlers, nexus
// tools, print-mode approval default) are appended by the caller. The
// project-config options (SYSTEM.md / APPEND_SYSTEM.md) must be applied
// BEFORE these so an explicit --system wins.
func harnessOptions(cfg *cliConfig) ([]harness.HarnessOption, error) {
	var opts []harness.HarnessOption

	roleLLM := func(flag, ref string, opt func(common.LLM) harness.HarnessOption) error {
		if ref == "" {
			return nil
		}
		def, err := resolveModel(ref)
		if err != nil {
			return fmt.Errorf("%s: %w", flag, err)
		}
		llm, err := llms.get(def)
		if err != nil {
			return fmt.Errorf("%s: %w", flag, err)
		}
		opts = append(opts, opt(llm))
		return nil
	}
	if err := roleLLM("--subagent-model", cfg.SubagentModel, harness.WithSubagentLLM); err != nil {
		return nil, err
	}
	if err := roleLLM("--blackboard-model", cfg.BlackboardModel, harness.WithBlackboardLLM); err != nil {
		return nil, err
	}
	if err := roleLLM("--advisor-model", cfg.AdvisorModel, harness.WithAdvisorLLM); err != nil {
		return nil, err
	}

	if cfg.MaxTokens != 0 || cfg.MaxIterations != 0 || cfg.MaxWallClock != 0 {
		opts = append(opts, harness.WithBudgets(budgets.Limits{
			MaxIterations: cfg.MaxIterations,
			MaxWallClock:  cfg.MaxWallClock,
			MaxTokens:     cfg.MaxTokens,
		}))
	}

	if cfg.SubagentDepthSet {
		opts = append(opts, harness.WithSubagentDepth(cfg.SubagentDepth))
	}
	if cfg.ApprovalTimeoutSet {
		opts = append(opts, harness.WithApprovalTimeout(cfg.ApprovalTimeout))
	}
	if cfg.NoPermissions {
		opts = append(opts, harness.WithPermissionsDisabled())
	}
	if cfg.SkipPermissions {
		opts = append(opts, harness.WithDangerouslySkipPermissions())
	}
	if cfg.ReadOnly {
		opts = append(opts, harness.WithReadOnly())
	}
	if cfg.ThinkingSet {
		opts = append(opts, harness.WithThinking(cfg.Thinking))
	}
	if cfg.NoSession {
		opts = append(opts, harness.WithSessionDisabled())
	}
	if cfg.NoContextFiles {
		opts = append(opts, harness.WithContextFilesDisabled())
	}

	if cfg.SystemFile != "" {
		data, err := os.ReadFile(cfg.SystemFile)
		if err != nil {
			return nil, fmt.Errorf("--system: read system prompt file: %w", err)
		}
		opts = append(opts, harness.WithSystemPrompt(strings.TrimSpace(string(data))))
	}

	if cfg.ConversationID != "" {
		opts = append(opts, harness.WithConversationID(cfg.ConversationID))
	}

	// resume: explicit ID wins; --continue looks up the newest session for
	// cwd. Applied after --conversation-id so --resume takes precedence.
	switch {
	case cfg.Resume != "" && cfg.ContinueLatest:
		return nil, fmt.Errorf("--resume and --continue are mutually exclusive")
	case cfg.Resume != "":
		opts = append(opts, harness.WithConversationID(cfg.Resume))
	case cfg.ContinueLatest:
		if cfg.NoSession {
			return nil, fmt.Errorf("--continue requires session persistence (drop --no-session)")
		}
		dir := cfg.sessionDir
		if dir == "" {
			dir = session.DefaultDir()
		}
		cwd := cfg.cwd
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("--continue: get working directory: %w", err)
			}
		}
		infos, err := session.List(dir, cwd)
		if err != nil {
			return nil, fmt.Errorf("--continue: find latest session: %w", err)
		}
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "note: no prior session for this directory, starting fresh")
		} else {
			opts = append(opts, harness.WithConversationID(infos[0].ConversationID))
		}
	}

	for _, s := range cfg.MCPServers {
		sc, err := parseMCPServer(s)
		if err != nil {
			return nil, err
		}
		opts = append(opts, harness.WithMCPServer(sc))
	}

	return opts, nil
}
