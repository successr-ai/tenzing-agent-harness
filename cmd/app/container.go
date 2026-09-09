package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	nexustools "github.com/successr-ai/tenzing-agent-harness/internal/app/nexus/tools"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	pkgmodels "github.com/successr-ai/tenzing-agent-harness/pkg/models"
	httpserver "github.com/tab58/huma-http-server"
	"github.com/tab58/huma-http-server/router"
)

// defaultModel is the model used when --model is not passed.
var defaultModel = pkgmodels.Ollama_GLM5_2_Cloud.(common.ModelDefinition)

type Config struct {
	ServerPort  int    `mapstructure:"SERVER_PORT" default:"8080"`
	LogDebug    bool   `mapstructure:"LOG_DEBUG"`
	NexusConfig string `mapstructure:"NEXUS_CONFIG" default:"nexus.yaml"`
	// Model overrides the default model as "provider/model-name"
	// (e.g. "ollama/qwen3.5-9b"); resolved against tenzing.yaml models:
	// entries and the built-in model set.
	Model string `mapstructure:"TENZING_MODEL"`
	// ProjectTrust is the default trust decision for directories without a
	// persisted trust.json entry: "trust" loads project-local config,
	// anything else skips it.
	ProjectTrust string `mapstructure:"TENZING_PROJECT_TRUST" default:"skip"`
}

// AppContainer wires all app-level dependencies for cmd/app: config,
// logging, the agent server (which owns the harness and LLM), and the
// HTTP server it is mounted on.
type AppContainer struct {
	cfg     *cliConfig
	cwd     string
	logFile *os.File
	logB    *app.LogBroadcaster
	api     *agentServer
	nexus   *nexus.Nexus
	server  *httpserver.Server[router.MapAuthInfo]
}

// NewAppContainer builds the container eagerly: config → cwd → logging →
// agent server (harness + event bus) → HTTP routes. Any failure after the
// log file opens closes it before returning.
func NewAppContainer(cfg *cliConfig) (*AppContainer, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	logB := app.NewLogBroadcaster()
	logFile, err := setupLogging(cwd, cfg.Debug, logB)
	if err != nil {
		return nil, err
	}

	model, err := resolveModel(cfg.Model)
	if err != nil {
		logFile.Close()
		return nil, err
	}

	bus := eventbus.NewEventBus()

	nexusCfg, err := nexus.LoadConfig(cfg.NexusConfig)
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("nexus config: %w", err)
	}

	// api is late-bound: the trigger's wake closure runs only after
	// nx.Start below, by which time api is set.
	var api *agentServer
	trig := nexus.NewTrigger(30*time.Second, func(channels []string) bool {
		if api == nil {
			return false
		}
		return api.startNexusTurn(channels)
	})

	var nx *nexus.Nexus
	if len(nexusCfg.Channels) > 0 {
		nx, err = nexus.New(nexusCfg, bus.Emit, trig.Notify)
		if err != nil {
			logFile.Close()
			return nil, fmt.Errorf("nexus init: %w", err)
		}
	}

	// Project trust (F26) gates the project-local drop-in config below.
	// --trust grants for this run only; otherwise the persisted decision or
	// TENZING_PROJECT_TRUST applies.
	trusted := cfg.Trust
	trustSource := "flag"
	if !trusted {
		trustSource = "error"
		trustPath, err := trustFilePath()
		if err != nil {
			slog.Warn("trust file path unavailable, treating project as untrusted", "error", err)
		} else {
			trusted, trustSource = resolveProjectTrust(trustPath, cwd, cfg.ProjectTrust)
		}
	}
	slog.Info("project trust resolved", "cwd", cwd, "trusted", trusted, "source", trustSource)

	// Drop-in config files: SYSTEM.md / APPEND_SYSTEM.md overrides (F24) and
	// prompt-template dirs (F22). Applied before harnessOptions so an
	// explicit --system wins over override files.
	cfgDir, _ := os.UserConfigDir()
	pc := loadProjectConfig(cwd, cfgDir, trusted)
	pc.logDecisions()

	extraOpts := pc.harnessOpts()
	cliOpts, err := harnessOptions(cfg)
	if err != nil {
		logFile.Close()
		return nil, err
	}
	extraOpts = append(extraOpts, cliOpts...)
	if nx != nil {
		extraOpts = append(extraOpts,
			harness.WithTool(nexustools.NewListChannelsTool(nx)),
			harness.WithTool(nexustools.NewReadChannelTool(nx)),
			harness.WithTool(nexustools.NewSearchChannelTool(nx)),
		)
	}

	api, err = newAgentServer(model, bus, nx, logB, trig.TurnEnded, models.pricing, extraOpts...)
	if err != nil {
		logFile.Close()
		return nil, err
	}
	api.models = models
	api.cwd = cwd
	api.bashAllow = cfg.BashAllow
	api.trustEnvDefault = cfg.ProjectTrust
	api.debug = cfg.Debug

	if nx != nil {
		nx.Start(context.Background())
	}

	server := httpserver.New(httpserver.ServerConfig{
		ServiceName:    "tenzing-agent",
		ServiceVersion: "0.1.0",
	}, router.MapAuthInfoBuilder)
	api.registerRoutes(server)

	slog.Info("container ready", "model", api.harness.GetCurrentModel(), "cwd", cwd, "tools", len(api.harness.ToolDefinitions()))

	return &AppContainer{
		cfg:     cfg,
		cwd:     cwd,
		logFile: logFile,
		logB:    logB,
		api:     api,
		nexus:   nx,
		server:  server,
	}, nil
}

// setupLogging opens the log file in dir and installs it as the slog
// default, teeing output to the /debug SSE broadcaster. Debug runs get a
// fresh timestamped file at trace level; normal runs append at info level.
// Serve mode passes the cwd; print mode passes printLogDir().
func setupLogging(dir string, debug bool, tee io.Writer) (*os.File, error) {
	name := ".tenzing-agent.log"
	level := slog.LevelInfo
	if debug {
		name = fmt.Sprintf(".tenzing-agent-%s.log", time.Now().UTC().Format("20060102T150405Z"))
		level = core.LevelTrace
	}

	logFile, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(logFile, tee), &slog.HandlerOptions{Level: level})))
	return logFile, nil
}

// Start runs the HTTP server until ctx is cancelled or the server fails.
func (ac *AppContainer) Start(ctx context.Context) error {
	errCh, err := ac.server.Start(fmt.Sprintf("127.0.0.1:%d", ac.cfg.Port))
	if err != nil {
		return fmt.Errorf("http server start: %w", err)
	}

	select {
	case e := <-errCh:
		if e != nil {
			return fmt.Errorf("http server: %w", e)
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}

// Shutdown stops nexus sources (so no new notifies can fire), cancels any
// in-flight turn, ends open SSE streams (they would otherwise block the
// graceful HTTP shutdown until its timeout), stops the HTTP server, the
// harness, the event bus (which ends the SSE forwarding goroutine), and
// closes the log file. Called once from main's defer.
func (ac *AppContainer) Shutdown() {
	if ac.nexus != nil {
		ac.nexus.Stop()
	}
	ac.api.cancelActiveTurn()
	ac.logB.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ac.server.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown", "error", err)
	}
	ac.api.harness.Shutdown()
	ac.api.bus.Close()
	ac.logFile.Close()
}

func (ac *AppContainer) Harness() *harness.Harness {
	return ac.api.harness
}

func (ac *AppContainer) Cwd() string {
	return ac.cwd
}

func (ac *AppContainer) Addr() string {
	return fmt.Sprintf(":%d", ac.cfg.Port)
}

// runServe builds the container and serves until ctx is cancelled.
func runServe(ctx context.Context, cfg *cliConfig) error {
	app, err := NewAppContainer(cfg)
	if err != nil {
		return fmt.Errorf("startup failed: %w", err)
	}
	defer app.Shutdown()

	fmt.Println("tenzing agent harness")
	fmt.Printf("  listen  http://localhost%s\n", app.Addr())
	fmt.Printf("  model   %s\n", app.Harness().GetCurrentModel())
	fmt.Printf("  cwd     %s\n", app.Cwd())
	fmt.Printf("  tools   %d registered\n", len(app.Harness().ToolDefinitions()))
	for _, t := range app.Harness().ToolDefinitions() {
		fmt.Printf("    - %s\n", t.Name)
	}
	fmt.Println()

	err = app.Start(ctx)
	// Restore default SIGINT handling: a second Ctrl+C during the shutdown
	// window below (HTTP graceful timeout + harness.Shutdown) now kills the
	// process instead of being swallowed by the NotifyContext registered in
	// main.
	signal.Reset(os.Interrupt)
	fmt.Println("shutting down (Ctrl+C again to force)")
	if err != nil {
		slog.Error("server ended with error", "error", err)
		return err
	}
	slog.Info("server stopped")
	return nil
}
