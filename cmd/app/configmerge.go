package main

import (
	"os"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
)

// defaultConfigPath is probed when neither --config nor TENZING_CONFIG names
// a file; missing there is fine (builtins and flags only).
const defaultConfigPath = "tenzing.yaml"

// resolveConfigPath picks the tenzing.yaml location: --config flag >
// TENZING_CONFIG env > ./tenzing.yaml. explicit reports whether the user
// named the path (flag or env) — a missing file is then a startup error
// instead of a silent skip.
func resolveConfigPath(flagValue string, flagChanged bool) (path string, explicit bool) {
	if flagChanged {
		return flagValue, true
	}
	if v := os.Getenv("TENZING_CONFIG"); v != "" {
		return v, true
	}
	return defaultConfigPath, false
}

// mergeConfigFile applies tenzing.yaml values under the precedence
// CLI flag > env var > file > default: a field is taken from the file only
// when its flag wasn't passed and (for the env-backed settings SERVER_PORT /
// LOG_DEBUG / NEXUS_CONFIG) the env var isn't set. The main model is merged
// separately in RunE, where its default chain lives.
//
// Mirrors mergeEnv's shape on purpose: flat, explicit, one line per field.
func mergeConfigFile(cfg *cliConfig, f cfgfile.File, changed func(name string) bool, present func(name string) bool) {
	setStr := func(flag string, dst *string, v string) {
		if v != "" && !changed(flag) {
			*dst = v
		}
	}
	setStr("subagent-model", &cfg.SubagentModel, f.SubagentModel)
	setStr("blackboard-model", &cfg.BlackboardModel, f.BlackboardModel)
	setStr("advisor-model", &cfg.AdvisorModel, f.AdvisorModel)
	setStr("system", &cfg.SystemFile, f.SystemFile)
	setStr("base-url", &cfg.BaseURL, f.BaseURL)
	setStr("api-key", &cfg.APIKey, f.APIKey)

	if f.AdvisorNudge != 0 && !changed("advisor-nudge") {
		cfg.AdvisorNudge = f.AdvisorNudge
	}
	if f.MaxTokens != 0 && !changed("max-tokens") {
		cfg.MaxTokens = f.MaxTokens
	}
	if f.MaxIterations != 0 && !changed("max-iterations") {
		cfg.MaxIterations = f.MaxIterations
	}
	if f.MaxWallClock != nil && !changed("max-wall-clock") {
		cfg.MaxWallClock = f.MaxWallClock.Value()
	}

	// Pointer fields: the file distinguishes "set to zero" from "omitted",
	// and setting them must also set the *Set marker or harnessOptions
	// ignores the value.
	if f.SubagentDepth != nil && !changed("subagent-depth") {
		cfg.SubagentDepth = *f.SubagentDepth
		cfg.SubagentDepthSet = true
	}
	if f.ApprovalTimeout != nil && !changed("approval-timeout") {
		cfg.ApprovalTimeout = f.ApprovalTimeout.Value()
		cfg.ApprovalTimeoutSet = true
	}
	if f.Thinking != nil && !changed("thinking") {
		cfg.Thinking = *f.Thinking
		cfg.ThinkingSet = true
	}

	setBool := func(flag string, dst *bool, v bool) {
		if v && !changed(flag) {
			*dst = true
		}
	}
	setBool("no-permissions", &cfg.NoPermissions, f.NoPermissions)
	setBool("dangerously-skip-permissions", &cfg.SkipPermissions, f.SkipPermissions)
	setBool("read-only", &cfg.ReadOnly, f.ReadOnly)
	setBool("no-session", &cfg.NoSession, f.NoSession)
	setBool("no-context-files", &cfg.NoContextFiles, f.NoContextFiles)

	// Env-backed settings: env wins over file.
	if f.Port != nil && !changed("port") && !present("SERVER_PORT") {
		cfg.Port = *f.Port
	}
	if f.NexusConfig != "" && !changed("nexus-config") && !present("NEXUS_CONFIG") {
		cfg.NexusConfig = f.NexusConfig
	}
	if f.Debug && !changed("debug") && !present("LOG_DEBUG") {
		cfg.Debug = true
	}

	// Additive: file servers mount alongside --mcp-server ones.
	for _, s := range f.MCPServers {
		cfg.MCPServerConfigs = append(cfg.MCPServerConfigs, mcp.ServerConfig{
			Name:    s.Name,
			Command: s.Command,
			Args:    s.Args,
		})
	}
}
