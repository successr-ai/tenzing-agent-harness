package main

import (
	"os"
	"path/filepath"
	"strings"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
)

// defaultConfigPath is probed when neither --config nor TENZING_CONFIG names
// a file; missing there is fine (builtins and flags only).
const defaultConfigPath = "tenzing.yaml"

// userConfigDir is the per-user tenzing directory, <UserConfigDir>/tenzing:
// ~/Library/Application Support/tenzing on macOS, $XDG_CONFIG_HOME/tenzing
// (default ~/.config/tenzing) elsewhere. It is the single home for the
// per-user tenzing.yaml, settings.json, trust.json, AGENTS.md and sessions/.
// Returns "" when the base dir is unavailable.
func userConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "tenzing")
}

// userConfigPath is the per-user fallback probed when there is no
// ./tenzing.yaml: <UserConfigDir>/tenzing/tenzing.yaml. Returns "" when the
// base dir is unavailable.
func userConfigPath() string {
	dir := userConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, defaultConfigPath)
}

// resolveConfigPath picks the tenzing.yaml location: --config flag >
// TENZING_CONFIG env > ./tenzing.yaml > <UserConfigDir>/tenzing/tenzing.yaml.
// explicit reports whether the user named the path (flag or env) — a missing
// file is then a startup error instead of a silent skip. The two probed
// paths stay non-explicit: absent is fine for both.
func resolveConfigPath(flagValue string, flagChanged bool) (path string, explicit bool) {
	if flagChanged {
		return flagValue, true
	}
	if v := os.Getenv("TENZING_CONFIG"); v != "" {
		return v, true
	}
	if _, err := os.Stat(defaultConfigPath); err != nil {
		if user := userConfigPath(); user != "" {
			if _, err := os.Stat(user); err == nil {
				return user, false
			}
		}
	}
	return defaultConfigPath, false
}

// resolveFilePaths rebases tenzing.yaml's path-valued keys onto the config
// file's own directory, so a config living outside the working directory can
// name files that sit beside it (e.g. system_file: SYSTEM.md next to a global
// <UserConfigDir>/tenzing/tenzing.yaml). Absolute paths are left alone. This applies
// only to values read from the file — CLI path flags stay cwd-relative, like
// every other shell argument.
//
// Returns a new File; the caller's copy is untouched.
func resolveFilePaths(f cfgfile.File, cfgPath string) cfgfile.File {
	dir := filepath.Dir(cfgPath)
	rebase := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}

	f.SystemFile = rebase(f.SystemFile)
	f.NexusConfig = rebase(f.NexusConfig)

	// A bare command name (no separator) is a PATH lookup, not a path —
	// "npx" must not become "<cfgdir>/npx". Only ./x, ../x and a/b rebase.
	servers := make([]cfgfile.MCPServer, len(f.MCPServers))
	copy(servers, f.MCPServers)
	for i, s := range servers {
		if strings.ContainsRune(s.Command, filepath.Separator) || strings.HasPrefix(s.Command, "./") {
			servers[i].Command = rebase(s.Command)
		}
	}
	f.MCPServers = servers

	return f
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

	if f.AdvisorNudge != 0 && !changed("advisor-nudge") {
		cfg.AdvisorNudge = f.AdvisorNudge
	}
	if f.MaxTurnTokens != 0 && !changed("max-turn-tokens") {
		cfg.MaxTurnTokens = f.MaxTurnTokens
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

	if f.Permissions != nil {
		policy := permissionPolicy(*f.Permissions)
		cfg.PermissionPolicy = &policy
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
