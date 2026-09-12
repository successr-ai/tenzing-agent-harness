package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tab58/huma-http-server/config"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
)

// exitCodeError carries a process exit code through cobra's error return.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

func newRootCmd() *cobra.Command {
	cfg := &cliConfig{}

	cmd := &cobra.Command{
		Use:   "tenzing",
		Short: "Tenzing agent harness — HTTP/SSE server by default, one-shot agent turn with -p",
		Long: "Runs the Tenzing agent harness.\n\n" +
			"Without -p: serves the HTTP/SSE app (today's behavior).\n" +
			"With -p \"prompt\": runs a single headless agent turn and exits.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Env fallback for env-configured settings (model/trust
			// defaults, serve settings).
			envCfg := &Config{}
			if err := config.Load(envCfg); err != nil {
				return fmt.Errorf("load env config: %w", err)
			}

			// tenzing.yaml: the single config file (--config >
			// TENZING_CONFIG > ./tenzing.yaml). Loaded first so its models:
			// section feeds the registry before any model resolution.
			cfgPath, explicit := resolveConfigPath(cfg.ConfigPath, cmd.Flags().Changed("config"))
			file, found, err := cfgfile.Load(cfgPath, explicit)
			if err != nil {
				return err
			}
			// Providers and models are declared, never compiled in, so
			// there is nothing to run without a config file.
			if !found {
				return fmt.Errorf("no config file found at %s: run 'tenzing init' to write a starter tenzing.yaml", cfgPath)
			}
			// Path-valued keys are relative to the config file, not the cwd,
			// so a global config can name files sitting beside it.
			file = resolveFilePaths(file, cfgPath)

			// --provider entries are merged over the file's by name, so a
			// flag can repoint one provider without restating the rest.
			d, err := buildDeps(file.Providers, file.Models, cfg.Providers)
			if err != nil {
				return fmt.Errorf("config %s: %w", cfgPath, err)
			}
			cfg.deps = d

			if cfg.ListModels {
				fmt.Fprint(cmd.OutOrStdout(), d.models.List())
				return nil
			}
			if cmd.Flags().Changed("prompt") && cfg.Prompt == "" {
				return errors.New("-p requires a non-empty prompt")
			}
			if cfg.OutputFormat != "text" && cfg.OutputFormat != "json" {
				return fmt.Errorf("--output-format must be text or json, got %q", cfg.OutputFormat)
			}
			if cfg.Prompt == "" && cmd.Flags().Changed("output-format") {
				return errors.New("--output-format requires -p")
			}

			// Effective model precedence: --model flag > tenzing.yaml
			// model:. There is no third source — the config declares every
			// model, so an unset model: has nothing to fall back to.
			if !cmd.Flags().Changed("model") {
				cfg.Model = file.Model
			}
			if cfg.Model == "" {
				return fmt.Errorf("no model selected: set model: in %s or pass --model", cfgPath)
			}
			// Validate the main model up front for both modes.
			if _, err := cfg.deps.resolve(cfg.Model); err != nil {
				return err
			}

			markSetFlags(cfg, cmd.Flags().Changed)

			// Env fallback for the three pre-existing env vars, then
			// tenzing.yaml values for whatever flags and env left unset.
			present := func(name string) bool {
				_, ok := os.LookupEnv(name)
				return ok
			}
			mergeEnv(cfg, envCfg, cmd.Flags().Changed, present)
			cfg.ProjectTrust = envCfg.ProjectTrust
			mergeConfigFile(cfg, file, cmd.Flags().Changed, present)

			// settings.json: per-command bash rules layered on the
			// name-level permission policy tenzing.yaml just produced.
			settingsPath, settingsExplicit := resolveSettingsPath(cfg.SettingsPath, cmd.Flags().Changed("settings"))
			bashRules, err := loadSettingsFile(settingsPath, settingsExplicit)
			if err != nil {
				return err
			}
			applyBashRules(cfg, settingsPath, bashRules)

			if cfg.Prompt != "" {
				// Warns on explicit CLI flags only — SERVER_PORT/NEXUS_CONFIG
				// env vars merged above stay silent by design (ambient env
				// shouldn't nag every print run).
				for _, name := range []string{"port", "nexus-config"} {
					if cmd.Flags().Changed(name) {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: --%s is ignored in print mode\n", name)
					}
				}
				return runPrintFn(cmd.Context(), cfg, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			if cmd.Flags().Changed("timeout") {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: --timeout is ignored in serve mode")
			}
			return runServe(cmd.Context(), cfg)
		},
	}

	cmd.AddCommand(newInitCmd())

	fl := cmd.Flags()
	fl.StringVar(&cfg.SettingsPath, "settings", "", "JSON per-command bash policy (default ./settings.json, then <user config dir>/tenzing/settings.json; env TENZING_SETTINGS)")
	fl.StringVar(&cfg.ConfigPath, "config", "", "YAML config file (default ./tenzing.yaml, then <user config dir>/tenzing/tenzing.yaml; env TENZING_CONFIG); CLI flags and env vars override its values")
	fl.StringVarP(&cfg.Prompt, "prompt", "p", "", "run one headless agent turn with this prompt, then exit (@path.png args attach images)")
	fl.StringVar(&cfg.OutputFormat, "output-format", "text", "print-mode output: text (final answer) or json (JSONL events)")
	fl.BoolVar(&cfg.ListModels, "list-models", false, "print the models declared in the config and exit")

	fl.StringVar(&cfg.Model, "model", "", `main model, named by a models: entry (see --list-models) or inline JSON naming a declared provider, e.g. '{"provider":"openrouter","model_name":"vendor/x","context_window":128000}' (model flags all accept both forms)`)
	fl.StringVar(&cfg.SubagentModel, "subagent-model", "", "model for subagents (default: main model)")
	fl.StringVar(&cfg.BlackboardModel, "blackboard-model", "", "model for blackboard llm_query (default: main model)")
	fl.StringVar(&cfg.AdvisorModel, "advisor-model", "", "model for the advisor tool; setting it enables the advisor and its write-gate")
	fl.IntVar(&cfg.AdvisorNudge, "advisor-nudge", 0, "iteration to start reminding an unconsulted executor to call advisor (0 = off; needs --advisor-model)")

	fl.Int64Var(&cfg.MaxTurnTokens, "max-turn-tokens", 0, "per-turn token budget (input+output cumulative), 0 = unlimited")
	fl.IntVar(&cfg.MaxIterations, "max-iterations", 0, "per-turn iteration budget, 0 = unlimited")
	fl.DurationVar(&cfg.MaxWallClock, "max-wall-clock", 0, "per-turn wall-clock budget, 0 = unlimited")

	fl.IntVar(&cfg.SubagentDepth, "subagent-depth", 1, "subagent nesting depth, 0 disables spawn_agent")
	fl.DurationVar(&cfg.ApprovalTimeout, "approval-timeout", 0, "tool-approval wait (serve default 120s; print default 0 = deny)")
	fl.BoolVar(&cfg.NoPermissions, "no-permissions", false, "disable permission gating entirely")
	fl.BoolVar(&cfg.SkipPermissions, "dangerously-skip-permissions", false, "auto-approve all approval prompts (sandboxes/pipelines)")
	fl.BoolVar(&cfg.ReadOnly, "read-only", false, "deny tools not marked read-only, no approval prompts")
	fl.BoolVar(&cfg.Thinking, "thinking", false, "model reasoning on or off (default: provider default)")
	fl.BoolVar(&cfg.NoSession, "no-session", false, "disable session persistence for this run")
	fl.BoolVar(&cfg.NoContextFiles, "no-context-files", false, "skip AGENTS.md context-file loading")

	fl.StringVar(&cfg.SystemFile, "system", "", "file whose contents replace the system prompt")
	fl.StringVar(&cfg.Resume, "resume", "", "resume the conversation with this ID (see GET /sessions or the session filenames)")
	fl.BoolVarP(&cfg.ContinueLatest, "continue", "c", false, "continue the most recent conversation for this directory")
	fl.BoolVar(&cfg.Trust, "trust", false, "treat the working directory as trusted for this run (loads ./SYSTEM.md, ./APPEND_SYSTEM.md, ./.tenzing/prompts); not persisted")
	fl.DurationVar(&cfg.Timeout, "timeout", 0, "print mode: abort the turn after this duration (e.g. 5m; 0 = no timeout)")

	fl.StringArrayVar(&cfg.MCPServers, "mcp-server", nil, `mount an MCP server, repeatable: "name=command arg1 arg2"`)
	fl.StringVar(&cfg.ConversationID, "conversation-id", "", "resume a prior conversation's memory")
	fl.StringArrayVar(&cfg.Providers, "provider", nil, `declare or override a provider as JSON, repeatable: '{"name":"ollama-cloud","type":"ollama","url":"https://ollama.com/","api_key":"$OLLAMA_API_KEY"}'; merged over tenzing.yaml by name`)

	fl.IntVar(&cfg.Port, "port", 8080, "serve-mode listen port (env SERVER_PORT)")
	fl.StringVar(&cfg.NexusConfig, "nexus-config", "nexus.yaml", "nexus channel config path (env NEXUS_CONFIG)")
	fl.BoolVar(&cfg.Debug, "debug", false, "trace-level logging to a fresh log file (env LOG_DEBUG)")

	return cmd
}

// markSetFlags records whether flags with "unset vs. zero" semantics were
// explicitly passed, so harnessOptions can tell a deliberate zero from a
// default.
func markSetFlags(cfg *cliConfig, changed func(name string) bool) {
	cfg.SubagentDepthSet = changed("subagent-depth")
	cfg.ApprovalTimeoutSet = changed("approval-timeout")
	cfg.ThinkingSet = changed("thinking")
}

// mergeEnv applies env-var fallback: an env value wins over the flag default,
// but an explicitly passed flag wins over env. Only the three pre-existing
// env vars participate. present reports whether the named env var is
// actually set — config.Load applies struct-tag defaults (e.g. port 8080)
// to env unconditionally, so env.ServerPort != 0 is true even when
// SERVER_PORT was never set; present is the real signal.
func mergeEnv(cfg *cliConfig, env *Config, changed func(name string) bool, present func(name string) bool) {
	if !changed("port") && present("SERVER_PORT") {
		cfg.Port = env.ServerPort
	}
	if !changed("debug") && present("LOG_DEBUG") {
		cfg.Debug = env.LogDebug
	}
	if !changed("nexus-config") && present("NEXUS_CONFIG") {
		cfg.NexusConfig = env.NexusConfig
	}
}
