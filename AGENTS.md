# Agent Guidelines — tenzing-agent-harness

Go module: `github.com/successr-ai/tenzing-agent-harness`

## Key Docs

- `SYSTEM_ARCHITECTURE.md` — source of truth for system design. Update when code changes affect harness structure, agent interface, tool system, provider layer, or config surface.

Project-specific rules for AI agents working in this codebase. General behavioral guidelines live in `CLAUDE.md`.

## Build & Test

```bash
go build ./...          # build
go test ./...           # unit tests
go test -race ./...     # race detector
go vet ./...            # static analysis
task app                # run the app (HTTP/SSE server)
```

No CI pipeline yet. Run `go build ./...` and `go test ./...` before declaring work done.

## Module & Imports

- Module path: `github.com/successr-ai/tenzing-agent-harness`
- Use short import aliases only when needed to disambiguate
- Internal packages import via `github.com/successr-ai/tenzing-agent-harness/internal/...`

## Layer Boundaries

Five layers, hexagonal dependency direction: **Core → Adapters → Features → Composition Root → App**.

| Layer             | Folder                                                              | Knows about                              | Does NOT know about                   |
| ------------------ | -------------------------------------------------------------------- | ----------------------------------------- | ------------------------------------- |
| Core               | `internal/core/` (+ `core/tooldef`)                                   | Domain types, all ports (`ModelPort`, `ToolPort`, `ContextPort`), extension/hook contracts (`Extensions`, hooks, `Decision`), the loop/FSM/turn/events, the `core.Agent` contract, the tool authoring contract (`tooldef.Definition`, `ExecutionContext`, `Schema`) | Everything else — adapters, features, harness, app |
| Adapters           | `internal/adapters/` (`agent`, `contextstore`, `eventbus`, `toolport`) | Core only; concrete implementations of core ports — `agent` implements `core.Agent`, `contextstore` implements `ContextPort`, `eventbus` implements `Emitter` (+ `Hooks` dispatch), `toolport` implements `ToolPort` (`Composite`/`Wrap`) plus the native `Registry` | Features, harness, app |
| Features           | `internal/features/<name>/` (plus `subagent.SpawnExt`, which lives in `harness/subagent` — see exception below) | Core only (+ `core/tooldef`); each package colocates its implementation with its `core.Extension` registration in `ext.go` | Other features, adapters, harness internals |
| Composition root   | `internal/harness/` (`harness.go`, `harness_options.go`, `memory.go`, `context_files.go`, `gate.go`; `runner/` facade; `subagent/` as a child composition root; `prompttmpl/` slash-command templates; `session/` persistence) | Everything below: builds adapters, the default + caller extension set, the composite ToolPort, assembles the system prompt, wires one runner per loop | Tool implementations' internals       |
| App                | `internal/app/nexus/` (+ `internal/app/` wiring, `cmd/app/`)          | Harness's public surface (`RunTurn`, `WithTool` registration) | —                                      |

**Never import upward.** Core imports nothing from `internal/` — it does import `pkg/common` for canonical message/tool types (`ProviderToolDefinition = common.ToolDefinition`), which is a boundary crossing to the shared type package, not an internal layering violation. `internal/` never imports `pkg/providers` or `pkg/models` — provider client construction is the caller's job (`cmd/app/llm.go`, or the library consumer). Adapters import core, never features or harness. Features import core only. Agent (`internal/adapters/agent`) doesn't import runner. If you need cross-layer communication, use an interface injected via config.

Two deliberate exceptions to "never import upward":

1. `harness.New` imports `internal/adapters/agent` in exactly one place — the unexported `defaultAgentBuilder` fallback — so a harness works out of the box with no brain injection. All other harness code talks only to the `core.Agent` interface. Callers with a custom brain override it via `harness.WithAgentBuilder(builder)`.
2. `internal/harness/subagent` is a **child composition root**: it deliberately imports both `internal/features/*` (to share the skills/blackboard extensions and assemble a child's extension set) and `internal/harness/runner` (to build the child's `AgentRunner`) — the same wiring job `harness.go` does for the main agent, done again per spawn.

### Blackboard REPL

The blackboard is strictly an agent→subagent communication mechanism: one-shot subagents deposit their findings before disconnecting, and the parent inspects them. It is not designed for long-lived agents to share results — contents are in-memory only and lost on any reset.

`internal/features/blackboard/` hosts the sandboxed Python REPL subprocess machinery (`repl.go`, `bootstrap.py`) plus the `Querier` interface and its LLM-backed implementation (`querier.go`) — the model-facing `rlm` tool and its offload path have been removed — alongside the `Blackboard` that builds on it: one persistent REPL per harness, shared by the main agent and all subagents through the `repl` tool. A single mutex serializes all access; write-own-slot is enforced in code — `bb` is a guard dict and creating/replacing a top-level key other than the executing agent's slot raises `PermissionError` (reads are unrestricted; the trusted Go `Deposit` path bypasses the guard). The blackboard's namespace (`bb` guard dict, `peek`, `bb_grep`) is injected via a setup exec (no blackboard-specific logic in `bootstrap.py`); `bootstrap.py`'s only related feature is a transport-level stdout cap (100k chars).

Known limit: `llm_query` inside the blackboard holds the REPL lock for all agents while it runs; keep individual calls small and prefer `llm_batch` for fan-out work. If this hurts, the upgrade path is an async callback queue — don't reach for it speculatively.

Cancellation or transport failure mid-call resets the blackboard (contents lost, lazily restarted empty); agents must tolerate missing slots.

Registration is via `internal/features/blackboard/ext.go` (package `blackboard`): a `core.Extension` providing the `repl` tool (`core.ToolProvider`, wrapping the same REPL tool implementation) and closing the blackboard on `core.SessionEndHook` (run by `Harness.Shutdown` with a 5s timeout). The `*blackboard.Blackboard` is built at the composition root and shared — main gets `blackboard.NewExt(bb, "main")`, each subagent gets `NewExt(bb, childID)`. Behavior unchanged.

## Adding Tools

All tools reach the model through the composite ToolPort (`internal/adapters/toolport/composite.go`), which mounts three source kinds in stable definition order: native registry tools (sorted by name), extension `core.ToolProvider` bundles (registration order), then `core.DynamicToolSource` snapshots (re-read at each turn's `BeginTurn`). Name collisions with an already-mounted tool are a construction error (dynamic collisions are skipped with a warning). The loop snapshots the port once per turn and passes the definitions into every `DoReasoning` call — the agent holds no tool state.

Two ways to add a tool:

1. **Native built-in** (file/shell-level tooling): create `internal/features/builtins/tool_<name>.go`, implement `tooldef.Definition` (`Name()`, `Description()`, `Schema()`, `Execute()`, from `internal/core/tooldef`), and add it to `builtins.Defaults()` — the composition root seeds every `toolport.NewRegistry` with that set — or inject via `harness.WithTool`.
2. **Extension tool**: implement `core.ToolProvider` on an extension — return `core.ToolSpec`s carrying their own `Execute` closure and an `Origin` like `"extension:<name>"`. No registry registration needed.

Notes:

- Tool descriptions are **instructions to the model**, not documentation — precise wording controls tool selection
- Tools never throw. Errors return `ToolResult{IsError: true}`; panics are recovered into error results by `Composite.Execute`. Loop doesn't break on tool errors
- Tools that perform no mutations should implement `tooldef.ReadOnlyReporter` (`ReadOnly() bool` → true; extension tools set `ToolSpec.ReadOnly`) — the loop runs consecutive read-only calls in a batch concurrently (bounded at 8), while unmarked tools act as barriers and run alone. The same marker gates `WithReadOnly` mode (unmarked tools are denied there). When in doubt, leave a tool unmarked

If the tool needs external state (skill registry, task graph), define a narrow interface in tooldef and accept it in the constructor. Don't import the concrete type.

## Adding Skills

1. Create `<skills-dir>/<name>/SKILL.md` with YAML frontmatter (default skills dir is `~/.claude/skills`; add more with `harness.WithSkillsDir`):
   ```yaml
   ---
   name: skill-name
   description: What the skill does and when to use it (full YAML — multi-line/folded values supported; `name` is required, `description` optional)
   ---
   ```
2. Body is loaded lazily via `load_skill` tool — no registration code needed
3. Skill metadata is discovered at startup from frontmatter only

UX unchanged; new plumbing: skills surface through `internal/features/skills/ext.go` (package `skills`) — a `core.Extension` providing the `list_skills`/`load_skill` tools (`core.ToolProvider`) and the "Available skills…" system-prompt index (`core.PromptContributor`, appended by the composition root). The agent knows nothing about skills. The same extension instance is shared with subagents via the factory config (read-only registry).

## MCP Servers

`harness.WithMCPServer(mcp.ServerConfig{Name, Command, Args, Env})` (repeat per server; re-exported as `tenzing.WithMCPServer`/`tenzing.MCPServerConfig`) mounts an external MCP server over stdio. The `mcp` extension (`internal/features/mcp`) connects on session start (a dead server logs a warning and serves zero tools — deliberately NOT load-bearing), closes on session end, and implements `core.DynamicToolSource`: tools are re-listed at each turn's `BeginTurn` with a 30s cache and surface as `mcp__<server>__<tool>` with `Origin: "mcp:<server>"`. Trust: the default permission policy's `AskOrigins: ["mcp:"]` escalates every MCP-origin tool to approval unless its full name is explicitly allow-listed. SSE/HTTP transports and `listChanged` subscriptions are follow-ons.

## Providers

The provider layer lives in this repo (no external LLM module):

- `pkg/common` — canonical types used throughout the harness: `common.LLM` (the client interface, including `GetModel() Model`), `Model`/`ModelDefinition`, `CompletionRequest`/`CompletionResponse`, `Message`, `ContentBlock`, `ToolDefinition`, streaming/stop-reason types, sentinel errors.
- `pkg/providers/protocols/` — protocol clients, each `NewClient(model Model, opts...) (common.LLM, error)`: `anthropic` (native SDK), `ollama` (native `/api/*` endpoints; diagnostics go to the default `slog` logger at Debug level), and `openai_compat` (OpenAI-compatible APIs — OpenAI, Cerebras, Lightning, OpenRouter — via `WithBaseURL`/`WithAPIKey`). API keys and base URLs are the caller's job — pass them via client options (`WithAPIKey`); nothing reads env vars in `pkg/`. Every protocol retries server 429s with exponential backoff by default (`ratelimit.NewDefaultBackoff` values; streaming retries only while no events have been emitted) — `WithRetryBackoff(ratelimit.RetryBackoff)` overrides the values, zero fields keep their defaults. Every protocol also takes the same two limiting options, off by default: `WithRateLimit(rate, burstSize)` (API token bucket; both values must be set or `NewClient` errors) and `WithMaxConcurrency(n)` (in-flight bound — inside the token bucket when combined with `WithRateLimit`, a pure semaphore alone) — `NewClient` wraps internally with `ratelimit.Wrap`, so callers never wrap themselves.
- `pkg/providers/protocols/ratelimit/` — `Limiter` primitives (`TokenBucket`, `Semaphore`), the `Wrap` decorator, `RetryBackoff`, and the shared 429 retry helpers (`IsRateLimited`, `Backoff`, `RetryOnRateLimit`, `RetryStreaming`) used by every protocol client. Internal plumbing for the protocol options; callers only touch `TokenBucketConfig` and `RetryBackoff`.
- `pkg/models` — convenience catalog of standard `common.Model` definitions (provider-prefixed vars; `models.Standard()` returns all as `[]ModelDefinition`). Convenience only, never a hard requirement: any `common.Model` implementation works with the protocol clients.

Models are `common.Model` values (`common.ModelDefinition{Name, MaxTokens, ContextWindowSize, DefaultContextWindow, Provider, SupportsThinking, SupportsVision}`), not strings. `Provider` is a plain string field ("anthropic", "ollama", ...) — pure metadata; there is no `Provider` type and nothing in the library dispatches on it. The harness never constructs clients — callers build a `common.LLM` from a protocol package and inject it. `cmd/app/llm.go` is the app-side factory: `buildLLM(def, baseURL, apiKey)` switches on `def.Provider` → protocol constructor, resolving keys from `--api-key` when passed, else the conventional env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `CEREBRAS_API_KEY`, `LIGHTNING_API_KEY`, `OPENROUTER_API_KEY`; Ollama keyless, `OLLAMA_API_KEY` optional), with a process-wide `llms` cache keyed (provider, model, baseURL). The cache carries the overrides (`llms.baseURL`/`llms.apiKey`, set in `root.go` RunE from `--base-url`/`--api-key` or tenzing.yaml `base_url`/`api_key`): `--base-url` beats tenzing.yaml `base_url` and the provider default endpoints.

## Constructing a Harness

`harness.New(mainLLM common.LLM, opts ...HarnessOption) (*Harness, error)` is the harness constructor — the caller builds the client (protocol package + model) and injects it. External consumers use the public facade `pkg/tenzing` — pure type/func aliases over `internal/harness` (plus the `runner`, `tooldef`, `core`, and `adapters/eventbus` types its options reference) exposing `tenzing.New` + `Harness.RunTurn` for a single programmatic loop. The LLM type layer is NOT re-exported — consumers import `pkg/common` (types) and `pkg/models` (standard definitions) directly; `pkg/tenzing` aliases only the harness surface. New harness options and Hooks event types must be re-exported in `tenzing.go` in the same change; new standard models go in `pkg/models` (including `models.Standard()`, which `cmd/app/models.go` derives `builtinModels()` from — see "CLI" below). The brain defaults to the built-in agent implementation (`internal/adapters/agent`); override with `WithAgentBuilder`.

`HarnessConfig` no longer exists. Behavior is configured via flat `HarnessOption` functions (`internal/harness/harness_options.go`):

- `WithAgentBuilder` — replaces the default agent implementation with a custom `runner.AgentBuilder` (the test seam for stub brains).
- `WithSubagentLLM` / `WithBlackboardLLM` / `WithAdvisorLLM` — per-role `common.LLM` clients. Unset subagent/blackboard roles fall back to the main LLM. `WithAdvisorLLM` has no fallback — it is the opt-in switch for the advisor feature: when set, the transcript-aware `advisor` tool (automatically sees the main conversation via the context store; takes one optional `question` argument) and its write-gate extension (`advisor.GateExt`, `internal/features/advisor/ext.go`) are both registered. The gate denies each turn's first state-changing tool call until `advisor` has been consulted (read-only tools flow freely; unknown/MCP tools count as state-changing; max 5 consults per turn) and contributes the advisor prompt guidance as a `PromptFragment`. `WithAdvisorNudge(n)` / `--advisor-nudge n` additionally reminds an unconsulted executor from iteration n onward (default off). Unset = no advisor tool, no gate, no prompt fragment.
- `WithTool` — injects an additional tool implementing `tooldef.Definition` (used by cmd/app to register nexus channel tools).
- Subagents (`spawn_agent` tool) are enabled by default at depth 1 using the main model; `WithSubagentDepth(0)` disables the tool.
- The shared blackboard REPL is **always on** (no disable option — the Python process boots lazily on first use): main agent and subagents share one persistent Python process (`repl` tool); subagent results over 2000 chars are deposited to `bb['<agent_id>']['result']` and returned as a 1500/500-char head/tail preview. Blackboard execs/deposits are logged via `slog` at info level (code capped 500 chars, stdout head 200).
- `WithPromptTemplatesDir(dir)` — registers a directory of `*.md` slash-command templates (repeatable); a `RunTurn` query of the form `/name args...` is expanded with bash-style argument substitution (`$1..$9`, `$@`/`$ARGUMENTS`, `${N:-default}`, `${@:N:L}`) before the loop sees it; an unknown `/name` fails the turn without an LLM call (`internal/harness/prompttmpl`).
- **Context files (default-on):** the global `<UserConfigDir>/tenzing/AGENTS.md` plus every `AGENTS.md` from the filesystem root down to cwd is appended to the main system prompt (path headers, 32KB cap; `internal/harness/context_files.go`). Main agent only — subagents keep their focused prompts. Opt out with `WithContextFilesDisabled()`.
- `WithToolCallGate(gate)` — installs a pre-execution veto (`func(ctx, core.ToolCall) error`) consulted before every tool call, main agent and all subagents (one shared gate, implemented as a `core.Extension` ToolCallHook registered right after permissions). A non-nil error blocks the call; the error string is fed back to the model as the tool result.
- `WithBudgets(budgets.Limits{MaxIterations, MaxWallClock, MaxTokens})` — graceful per-turn termination (`terminated: <reason>` error from `RunTurn`); zero fields unlimited. Subagents get `MaxIterations` from `WithSubagentMaxIterations` (default 100) via the same extension.
- `WithMCPServer(cfg)` — mounts an external MCP server (see "MCP Servers" above); repeat per server.
- **Permissions (default-on):** the permissions extension gates tool calls by name — the default policy (`permissions.DefaultPolicy`) escalates code-executing/file-writing tools (`bash`, `write`, `edit`, `repl`, `spawn_agent`) to `AskUser` (`advisor` is deliberately not listed: it is read-only and the advisor write-gate mandates calling it, which must not hang on approval in headless mode); the loop then emits `ApprovalRequestedEvent` and blocks up to `WithApprovalTimeout` (default 120s; 0 = deny immediately) for a `Respond(bool)`. Override the policy with `WithPermissionPolicy`, opt out entirely with `WithPermissionsDisabled()`, or auto-approve with `WithDangerouslySkipPermissions()` (CLI `--dangerously-skip-permissions`) — that mode keeps every hook in place but resolves any `AskUser` decision in the main loop to `Allow` without emitting an approval request (`Deny` still denies; for Docker sandboxes / CI pipelines with nobody to answer prompts; implemented as `LoopConfig.SkipPermissions`). `WithReadOnly()` (CLI `--read-only`) is the unattended-safe mode: it REPLACES the permissions extension with a hook in `internal/harness/readonly.go` (shared with subagent loops) that denies every call whose tool is not marked read-only per `Composite.ReadOnly` (unmarked/unknown = mutating; `bash`, `write`, `edit` all deny) — no AskUser escalation exists in this mode (`WithPermissionPolicy` is ignored, MCP tools' own read-only claims are trusted) and the approval timeout is forced to 0, so no prompt or timeout ever fires. Exemptions: `advisor` is marked read-only (pure LLM call); `repl` is marked read-only (its sandbox blocks file writes — only shared in-memory blackboard state mutates); `spawn_agent` is allowed by name because child loops carry the same gate — spawned agents cannot touch the filesystem, only shared in-memory blackboard state. Ordering guarantee: permissions run FIRST among extensions, and later hooks can escalate but never lower its decision. **Migration note:** consumers driving the harness headlessly (no `OnApprovalRequested` hook, no cmd/app UI) must either call `WithPermissionsDisabled()` or handle the approval event — otherwise mutating tools are denied after the approval timeout. Subagent loops carry no permissions extension (the parent's spawn was already approved).
- `WithThinking(enabled)` — toggles model reasoning for the main agent's requests (default: provider default). Runtime toggle: `Harness.SetThinking` (between turns; emits `ThinkingChangedEvent`).
- `WithLLMRetry(max, baseDelay)` — tunes the default agent's transient-LLM-error retry policy (default 3 retries / 2s base, exponential backoff + jitter; negative max disables). Network errors, timeouts, and 5xx/429/overload retry; 4xx does not; a streaming call that already emitted deltas is never retried. Each retry emits `LLMRetryEvent`.
- `WithCompressionThreshold(frac)` / `WithCompressionKeepMessages(n)` — tune the main context store's auto-compression (defaults 0.75 of context window / 6 messages kept).
- **Runtime controls (between turns only; `errBusy` mid-turn):** `Harness.Compact(ctx, instructions)` forces context compression via the context store (works with any brain); `Harness.SetLLM(llm)` switches the main agent to a caller-built client and emits `ModelChangedEvent` (`CurrentModel()` reads back the active `common.Model` via `llm.GetModel()`; client reuse/caching is the caller's job — cmd/app's `llms` cache makes switch-back free); `Harness.SetThinking(bool)` toggles reasoning and emits `ThinkingChangedEvent`. SetLLM/SetThinking require the default brain (custom `WithAgentBuilder` brains get a clean error unless they implement `SetLLM`/`SetThinking`).
- **Prompt caching:** every default-agent request sets `CacheSystemAndTools` (Anthropic caches system prompt + tool schemas; other providers ignore it); cache token counts surface on `LLMResponseEvent` (`cache_read_input_tokens`/`cache_creation_input_tokens`).
- `WithConversationID(id)` — resumes a prior conversation: the main agent runs under `id` instead of a random one. When session persistence is on (the default), `harness.New` loads that conversation's latest session JSONL file (same cwd), reconstructing message-level history into the main `contextstore.Store`'s `InitialHistory` (tool outputs capped at 4KB on replay; compaction entries restart the replay from their summary plus the most recent reconstructed messages) and restoring the todo plan from the latest snapshot entry; the compression-summary memory file remains the fallback (seeded as `InitialMemory`) for conversations without a session file. Omit to start a new conversation under a random ID (`Harness.ConversationID()` returns it either way — the caller's handle for a future resume).
- **Session persistence (default-on):** `internal/harness/session/` — a persister subscribed to the event bus appends main-agent events as JSONL entries to `<UserConfigDir>/tenzing/sessions/<cwd-hash>/<timestamp>_<conversationID>.jsonl` (header line, then user / assistant / tool_result / steering / compaction / todo-snapshot entries with an `id`+`parent_id` chain; images go to content-addressed sidecar blobs). Append-only and best-effort: a write failure logs a warning and disables the store for the process; subagent events are filtered out. `WithSessionDisabled()` opts out, `WithSessionDir(dir)` relocates; `Harness.SessionInfo()` reports the resolved dir + cwd; `session.List/Delete/Rename` manage files.

The harness holds no LLM cache — it uses exactly the clients injected via `New`/`With*LLM`/`SetLLM`. cmd/app caches clients per (provider, model, base URL) in `cmd/app/llm.go` so roles sharing a model share one client.

Memory (`internal/harness/memory.go`) is separate from the in-process `contextstore.Store`: it's the harness's own `ContextCompressedEvent` subscriber, persisting each compaction's summary to a per-conversation file (`.agent_memory-<stamp>-<runnerID>.md`, main-agent files in the OS config dir, subagent files in the OS cache dir) so a later `WithConversationID` resume — even in a new process — has something to load. Neither `internal/core` nor `internal/adapters/contextstore` know this file-backed store exists; they only emit/consume the event.

## CLI (`cmd/app`)

`cmd/app` is a single cobra command, `tenzing` (`cmd/app/root.go`). No `-p/--prompt` → serves the HTTP/SSE app (`runServe`, `NewAppContainer`). With `-p "prompt"` → one headless agent turn then exit (`cmd/app/print.go`): `--output-format text` (default, final answer only) or `json` (JSONL — every harness event plus text/thinking deltas, with a `{"type":"result",...}` line guaranteed last, written only after the event bus is closed and drained). The JSONL schema is the versioned wire contract in `internal/app/wire`: every line is an `Envelope` (`v`, `type`, `ts`, `runner_id`, typed `data` payload), core and nexus events are explicitly mapped (durations converted to real milliseconds — core's `duration_ms` tags marshal nanoseconds), unmapped events become `unknown_event` lines instead of leaking core struct shapes, and any breaking payload change requires a `wire.Version` bump. The serve-mode SSE stream builds its payloads from the same envelopes (`translateSSE` in `cmd/app/server.go`, adding a top-level `agent` label; the embedded UI in `cmd/app/index.go` parses those shapes). RPC mode (PI item 13) reuses this package. Exit codes: `0` success, `1` config/startup error, `2` turn failure. Headless permission default is `WithApprovalTimeout(0)` (deny mutating tools immediately) unless `--approval-timeout` was explicitly passed or `--no-permissions` is set; denied calls emit `core.ToolDeniedEvent` (`tool.denied`), print mode counts them and reports a stderr summary plus a `denied_tools` field on the json result line — exit code stays 0. Event delivery is lossless under stdout backpressure: an unbounded in-memory queue (`eventQueue` in `cmd/app/print.go`) sits between the 256-slot bus subscription and the stdout writer — the drain side never blocks, so the bus's drop-on-full `Emit` never fires for a slow consumer. A *failed* stdout write (consumer closed the pipe) cancels the in-flight turn — no tokens are burned streaming into a void. An explicitly empty prompt (`-p ""`) is a config error, not serve mode.

~28 flags parse into `cliConfig` (`cmd/app/options.go`); `--config` (default `./tenzing.yaml`, env `TENZING_CONFIG`) loads the unified config file whose values fill flags-and-env-left-unset fields (`internal/config` schema + `mergeConfigFile` in `cmd/app/configmerge.go`; MCP servers from the file land pre-parsed in `cfg.MCPServerConfigs`, additive with `--mcp-server`); `harnessOptions(cfg)` maps the resolved config to the same `HarnessOption`s for both modes — model flags (`--model`, `--subagent-model`, `--blackboard-model`, `--advisor-model`, resolved via `cmd/app/models.go`, see below), budgets (`--max-tokens`, `--max-iterations`, `--max-wall-clock`), toggles (`--advisor-nudge` (warned + ignored without `--advisor-model`), `--subagent-depth`, `--approval-timeout`, `--no-permissions`, `--dangerously-skip-permissions`, `--read-only`, `--thinking`, `--no-session`, `--no-context-files`), prompt/session/trust flags (`--system` file replacing the system prompt, `--resume <id>`, `-c/--continue` for the latest session — mutually exclusive with `--resume`, requires persistence — `--trust` one-shot trust grant, `--timeout` print-mode turn deadline), wiring (`--mcp-server` repeatable, `--conversation-id`, `--base-url`/`--api-key` LLM-client overrides applied to the `llms` cache in RunE), and serve-only flags (`--port`, `--nexus-config`) — passing those with `-p` prints a stderr warning and they are otherwise ignored (as is `--timeout` without `-p`). Print prompts may carry `@path.png`-style tokens (png/jpg/jpeg/gif/webp) that attach the files as images via `RunTurnWithImages`. `--debug` is shared by both modes — it never touches stdout, but it does raise the log file to trace level and switch it to a fresh timestamped filename (`setupLogging`), in print mode same as serve mode. Log location differs: serve mode logs in the cwd, print mode under `os.UserCacheDir()/tenzing/` (`printLogDir` in `cmd/app/print.go`, temp-dir fallback). `--subagent-depth`, `--approval-timeout`, and `--thinking` have unset-vs-zero semantics (0/false are valid explicit values), so `markSetFlags` records cobra's `Changed()` into `cfg.SubagentDepthSet`/`ApprovalTimeoutSet`/`ThinkingSet`.

Setting precedence: explicit flag > env var > tenzing.yaml > default. `mergeEnv` (`root.go`) applies the three pre-existing env vars (`SERVER_PORT`, `LOG_DEBUG`, `NEXUS_CONFIG`); `mergeConfigFile` (`cmd/app/configmerge.go`) then fills whatever flags and env left unset from the config file — `internal/config` owns the tenzing.yaml schema (`config.File`, strict parsing, unknown keys are startup errors; scalars whose zero is meaningful are pointers so the merge can set the `*Set` markers). The file is located via `--config` > `TENZING_CONFIG` > `./tenzing.yaml` (`resolveConfigPath`); missing at the default path is fine, missing at an explicit path errors. Two further env-only settings load via the same `Config` struct: `TENZING_MODEL` and `TENZING_PROJECT_TRUST` (default `skip`). Main-model precedence: `--model` flag > `TENZING_MODEL` > tenzing.yaml `model:` > tenzing.yaml `models.default` > compiled default (`ollama/glm-5.2:cloud`).

`NewAppContainer(cfg *cliConfig)` builds the HTTP/SSE app from the resolved `cliConfig` — trust resolution + drop-in project config (`cmd/app/trust.go`, `projectconfig.go`: SYSTEM.md/APPEND_SYSTEM.md/.tenzing/prompts; project beats global, replace beats append, project-local sources trust-gated, explicit `--system` wins), then the same `harnessOptions(cfg)` print mode uses, plus nexus wiring and the port/nexus-config serve-only fields (`--debug` is shared, not serve-only — see above). The registry's tenzing.yaml pricing feeds the server's `costTracker` (`cmd/app/costs.go`; `GET /stats` + `cost` SSE event).

Model resolution: `cmd/app/models.go` is a registry — custom entries from tenzing.yaml's `models:` section (base_url/vision/cost per entry, optional `default:` ref; `buildRegistry(config.ModelsSection)`, pure, no I/O) layered over `builtinModels()` derived from `models.Standard()` (`pkg/models`) — no manual sync. `resolveModel`/`modelList`/`--list-models` resolve `provider/name` refs against both. A ref starting with `{` is instead an inline definition — stringified JSON with the `config.ModelEntry` fields, validated/defaulted by the shared `defFromEntry` (base_url/cost fields ignored inline) — accepted by every model flag. Resolved definitions become clients via the `llms` cache (`cmd/app/llm.go`), which reads the registry's base URLs.

Serve mode's `agentServer` (`cmd/app/server.go`) queues concurrent queries instead of rejecting them: `/query` returns `started` or `queued` (FIFO, drained in order by `finishTurn` chaining; `/cancel` drops the queue), and exposes `/steer`, `/state`, `/sessions` (GET/DELETE/PATCH), `/messages`, `/compact`, `/thinking`, `/model`, `/models`, `/stats`, and `/trust` (GET/POST) alongside the pre-existing routes — see `SYSTEM_ARCHITECTURE.md` §16 for the endpoint table.

## Configuration & DI

All non-invariant runner behavior flows through `runner.AgentRunnerOption` functional options (`internal/harness/runner/agent_runner.go`), passed to `runner.NewAgentRunner(agent, opts...)`. To change runner behavior:

- Swap the Agent (different model/provider)
- Swap the ToolRegistry (`WithToolRegistry`, different native tool set) or the whole ToolPort (`WithToolPort` — the harness passes the composite here; unset falls back to wrapping the registry)
- Swap the ContextStore (`WithContextStore`) — required; the `core.ContextPort` implementation that owns this runner's conversation history
- Swap the SystemPrompt (`WithSystemPrompt`)
- Provide a `core.Emitter` to receive structured events from the loop (`WithEmitter`)
- Provide `WithTextDeltaHandler`/`WithThinkingDeltaHandler` callbacks for streaming text — `func(runnerID, text string)`; the runner tags each delta with its own id so multiplexed consumers (RPC mode) can correlate deltas per turn

System reminders (todo state, etc.) no longer flow through the runner. They
are injected each iteration by `core.Extension`s implementing
`core.BeforeIterationHook` (e.g. `internal/features/reminders`), registered
on `*core.Extensions` and passed to the runner via `runner.WithExtensions`.
`harness.New` wires the default extension set — reminders, skills
(`skills.Ext`), blackboard (`blackboard.Ext`, unless disabled), and subagents
(`subagent.SpawnExt`, when depth > 0) — then appends caller extensions from
`harness.WithExtension` (also re-exported as `tenzing.WithExtension` with the
`Extension`/hook interfaces and `ToolSpec`).

Don't modify the loop. Don't add fields to the `AgentRunner` struct. Configure via `runner.AgentRunnerOption`.

## FSM Rules

Six states, six transitions. Don't add states or transitions without updating `SYSTEM_ARCHITECTURE.md`.

The FSM lives in `internal/core/fsm.go` and is per-Loop instance — subagents and concurrent loops don't share state.

## File Conventions

| Pattern                  | Location                                              |
| ------------------------ | ----------------------------------------------------- |
| Core domain (types/FSM/events/loop/ports) | `internal/core/` |
| Port adapters (agent, contextstore, eventbus, toolport) | `internal/adapters/` |
| Features (core.Extension implementations) | `internal/features/<name>/` |
| Tool authoring contract  | `internal/core/tooldef/tooldef.go`                    |
| Built-in tool implementations | `internal/features/builtins/tool_*.go`          |
| Provider implementations | `pkg/providers/protocols/` (+ `pkg/common` types, `pkg/models` catalog) |
| Prompt templates         | `internal/features/prompts/*.gotmpl`                   |
| REPL subprocess machinery | `internal/features/blackboard/` (Python REPL, Querier) |
| Context management       | `internal/adapters/contextstore/` (implements `core.ContextPort`: history, tool_use/tool_result pairing, compression) |
| App (cobra CLI: HTTP/SSE server by default, `-p` for one-shot print) | `cmd/app/` |
| Public API facade        | `pkg/tenzing/` (aliases over `internal/harness`)      |
| Test files               | Same directory as source, `*_test.go`                 |
| Shared test helpers      | `**/testutil_test.go`                                 |
| Sub-agent system         | `internal/harness/subagent/`                          |
| Blackboard (shared REPL)  | `internal/features/blackboard/` (persistent REPL, repl tool) |
| Event system             | `internal/core/` (vocabulary: `Event`, `EventType`, `BaseEvent`, `Emitter`); `internal/adapters/eventbus/` (`EventBus`, `Hooks`) |
| Embedded assets          | Adjacent to consumer (e.g. `blackboard/bootstrap.py`) |
| Session persistence      | `internal/harness/session/` (JSONL store, persister, list/load) |
| Prompt template expansion | `internal/harness/prompttmpl/` (slash-command registry + `$1`-style expansion) |
| Nexus (input channels)   | `internal/app/nexus/`                                 |
| Nexus channel tools      | `internal/app/nexus/tools/`                           |
| JSONL wire contract      | `internal/app/wire/` (versioned event envelopes for `--output-format json` / SSE) |
| App-level wiring helpers | `internal/app/` (log SSE broadcaster)                 |

## Testing

- Table-driven tests as standard pattern
- Test files live next to source
- Use `testutil_test.go` for shared helpers within a package
- Mock via interfaces, not concrete types
- `go test -race ./...` catches concurrency bugs — run before any change touching goroutines, channels, or shared state

## Common Mistakes to Avoid

- **Mutating the loop.** New capabilities = new tools or new config, never loop changes
- **Importing upward.** Tools → harness or agent → runner = architecture violation
- **Wide interfaces in tools.** Tools accept narrow interfaces, not concrete types
- **Hardcoding provider behavior.** All provider differences stay in the provider layer; canonical types above
- **Forgetting `SYSTEM_ARCHITECTURE.md`.** If your change affects harness structure, agent interface, tool system, provider layer, or config surface — update the architecture doc in the same PR
