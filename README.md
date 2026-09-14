# tenzing-agent-harness

An AI agent harness built in Go. The harness is the environment around the model — the loop, tools, context management, and orchestration — not a framework. The core loop (perception → action → observation) never changes; capabilities grow by registering tools and layering mechanisms around the loop.

## Architecture

Hexagonal (ports & adapters), five layers with strict dependency direction — **core imports nothing, adapters import core, features import core, harness imports everything**:

- **Core** (`internal/core/`) — domain vocabulary, all port interfaces, the reasoning loop and FSM
- **Adapters** (`internal/adapters/`) — one package per port implementation (agent, contextstore, eventbus, toolport)
- **Features** (`internal/features/`) — self-contained capabilities, each registering itself via a colocated `ext.go`
- **Composition root** (`internal/harness/`) — builds adapters + features, wires the loop
- **App** (`cmd/app`, `internal/app/`, `pkg/tenzing/`) — entrypoints and the public facade

See `SYSTEM_ARCHITECTURE.md` for the full design.

## Providers

Provider-agnostic via canonical types in `pkg/common` (`LLM`, `Model`, `Message`, `ContentBlock`, `CompletionRequest/Response`). The harness never constructs clients — you build a `common.LLM` from a protocol package and inject it (`tenzing.New(llm)`), so any backend that speaks one of the protocols works.

Protocol clients (`pkg/providers/protocols/`), each `NewClient(model, opts...) (common.LLM, error)`:

- `anthropic` — native Anthropic SDK
- `ollama` — native Ollama `/api/*` endpoints (local or Ollama Cloud)
- `openai_compat` — any OpenAI-compatible API: OpenAI, OpenRouter, Groq, a local vLLM, ... (point it with `WithBaseURL`)

Models are values implementing `common.Model`, not strings. `pkg/models` ships a catalog of standard definitions for convenience (`models.Anthropic_ClaudeSonnet4_6`, `models.OpenRouter_KimiK3`, ...), but any `common.Model` implementation — including an inline `common.ModelDefinition` — works the same. API keys and base URLs are yours to supply via client options; the library reads no env vars.

Every protocol retries server 429s with exponential backoff by default (`ratelimit.NewDefaultBackoff()` values); `WithRetryBackoff(ratelimit.RetryBackoff)` overrides the values. Every protocol also takes the same two limiting options, off by default: `WithRateLimit(rate, burstSize)` for a client-side token bucket (both values required) and `WithMaxConcurrency(n)` to bound in-flight requests. The wrapping happens inside `NewClient` — the returned `common.LLM` is already guarded.

## Library Usage — OpenRouter example

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/models"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/openai_compat"
	"github.com/successr-ai/tenzing-agent-harness/pkg/tenzing"
)

func main() {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_API_KEY not set")
	}

	// A standard model from the catalog (pkg/models)...
	llm, err := openai_compat.NewClient(models.OpenRouter_KimiK3,
		openai_compat.WithName("openrouter"),
		openai_compat.WithBaseURL("https://openrouter.ai/api/v1"),
		openai_compat.WithAPIKey(key),
	)
	if err != nil {
		log.Fatal(err)
	}

	// ...or any model OpenRouter serves, defined inline.
	deepseek := common.ModelDefinition{
		Name:              "deepseek/deepseek-chat-v3.1",
		Provider:          "openrouter",
		ContextWindowSize: 131072,
		MaxTokens:         32768,
	}
	fast, err := openai_compat.NewClient(deepseek,
		openai_compat.WithName("openrouter"),
		openai_compat.WithBaseURL("https://openrouter.ai/api/v1"),
		openai_compat.WithAPIKey(key),
	)
	if err != nil {
		log.Fatal(err)
	}

	h, err := tenzing.New(llm,
		tenzing.WithSubagentLLM(fast), // cheaper model for spawned subagents
		tenzing.WithPermissionsDisabled(),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Shutdown()

	answer, err := h.RunTurn(context.Background(), "summarize README.md")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)
}
```

The same pattern serves the other roles: `WithSubagentLLM` and `WithBlackboardLLM` take any `common.LLM`; unset roles fall back to the main client. `WithAdvisorLLM` is different — it opts in to the advisor feature: a transcript-aware `advisor` tool (a stronger model that automatically sees the full conversation) plus a write-gate that requires consulting it before each turn's first state-changing tool call. Switch the main model between turns with `Harness.SetLLM(otherLLM)`.

## Features

- **Tool system** — bash, Read, Write, Edit, Grep, Glob, ls; Edit and overwriting Write enforce read-before-edit via per-registry FileTracker stamps, with atomic per-path-locked writes
- **Skill system** — lazy-loaded domain knowledge via YAML-frontmatter Markdown files
- **Subagents** — spawn isolated agent loops with fresh context; only the final summary returns to the parent
- **Context compression** — three-layer system: recent messages kept verbatim, older messages summarized via LLM, summaries persisted per conversation to `<UserConfigDir>/tenzing/.agent_memory-<date>-<agent-id>.md` (resume with `WithConversationID`)
- **Shared blackboard REPL** — one persistent, sandboxed Python REPL shared by the main agent and subagents, for processing inputs beyond the context window (`llm_query`/`llm_batch` sub-LLM calls in loops over shared state). Strictly an agent→subagent channel: one-shot subagents deposit findings before disconnecting; not designed for long-lived agents to share results
- **Permissions & read-only mode** — code-executing/file-writing tools require approval by default (`ApprovalRequestedEvent`, `POST /approve`, 120s timeout); `--read-only` / `WithReadOnly()` instead denies every tool not marked read-only with no prompts ever — reads, `advisor`, and `spawn_agent` (children equally gated) still run; `--no-permissions` / `WithPermissionsDisabled()` disables gating entirely
- **Todo planning** — model commits a plan before acting (dependency-aware, in-memory task board, one plan per harness or subagent), progress re-injected as reminders after every tool call
- **Session persistence** — conversations recorded as JSONL per working directory; resume with `--resume <id>` or `-c` (latest), manage over HTTP (`GET/DELETE/PATCH /sessions`, `GET /messages`)
- **Unified config file** — `tenzing.yaml` holds every durable setting (models incl. the custom-model registry, advisor, subagent, budgets, permissions, MCP servers, serve settings); `--config` / `TENZING_CONFIG` pick the file (default `./tenzing.yaml`, then `<user config dir>/tenzing/tenzing.yaml`), precedence CLI flag > env var > file > default; model flags alternatively take an inline JSON definition (see Quick Start)
- **Project config & trust** — `./SYSTEM.md` replaces / `./APPEND_SYSTEM.md` appends to the system prompt, `./.tenzing/prompts` adds slash-command templates; project-local files load only for trusted directories (`--trust`, `POST /trust`, or `TENZING_PROJECT_TRUST=trust`), global `<UserConfigDir>/tenzing/` equivalents always load
- **Cost tracking** — token usage (incl. prompt-cache tokens) and USD cost per model, priced from `tenzing.yaml` model costs; `GET /stats` + a `cost` SSE event
- **Vision** — image input on vision-capable models: `@path.png` args in `-p` prompts, `images[]` on `POST /query`, paste/drag-drop in the chat UI

## Prerequisites

- Go 1.25.9+
- [Task](https://taskfile.dev) (optional, for `task app`)
- Python 3 (for the blackboard REPL)

## Quick Start

```bash
# Build
go build ./...

# Run the app (HTTP/SSE server with embedded chat UI)
task app
# or directly:
go run ./cmd/app

# One-shot programmatic run
go run ./cmd/app -p "summarize README.md"
task ask -- "summarize README.md"

# JSONL event stream
go run ./cmd/app -p "..." --output-format json

# Pick a model (by the name a models: entry declares) / set budgets
go run ./cmd/app -p "..." --model main-model --max-turn-tokens 50000
go run ./cmd/app --list-models

# Declare or repoint a provider from the command line, repeatable; merged
# over tenzing.yaml's providers: by name ("$VAR" expands in your shell).
# type is optional and defaults to openai_compat
go run ./cmd/app -p "..." \
  --provider '{"name":"groq","url":"https://api.groq.com/openai/v1","api_key":"'"$GROQ_API_KEY"'"}'

# Inline model definition: every model flag (--model, --subagent-model,
# --blackboard-model, --advisor-model) also accepts stringified JSON with the
# model entry fields, naming a provider declared in the config or by
# --provider; omitted context_window/max_response_tokens default to 128k/32k
go run ./cmd/app -p "..." \
  --model '{"provider":"ollama-cloud","model_name":"glm-5.3-flash","context_window":1048576}'

# Load everything from a config file (default ./tenzing.yaml, see below)
go run ./cmd/app -p "..." --config my-setup.yaml

# Sessions: continue the latest conversation for this directory, or a specific one
go run ./cmd/app -p "and now?" -c
go run ./cmd/app -p "and now?" --resume <conversation-id>
go run ./cmd/app -p "ephemeral" --no-session

# Prompt/behavior controls
go run ./cmd/app -p "..." --system prompt.md      # file replaces the system prompt
go run ./cmd/app -p "..." --thinking=false        # toggle model reasoning
go run ./cmd/app -p "..." --no-context-files      # skip AGENTS.md loading
go run ./cmd/app -p "..." --trust                 # load ./SYSTEM.md etc. this run
go run ./cmd/app -p "..." --timeout 5m            # abort the turn after 5m
go run ./cmd/app --read-only                      # deny mutating tools, no approval prompts

# Attach images (vision-capable models): @path tokens in the prompt
go run ./cmd/app -p "describe @screenshot.png"
```

Endpoints and API keys come from the config file's `providers:` section — the CLI reads no provider env vars of its own. Keep the secret in the environment and reference it: `api_key: "$OPENROUTER_API_KEY"` expands at load time. **`tenzing.yaml` is required**: providers and models are declared, never compiled in, so a first run without one fails with a pointer to `tenzing init`. Optional env: `TENZING_CONFIG` (tenzing.yaml path, default `./tenzing.yaml` then `<user config dir>/tenzing/tenzing.yaml`), `TENZING_PROJECT_TRUST` (`trust` to load project-local config by default).

## Config file (`tenzing.yaml`)

One YAML file for every durable setting. `tenzing init` writes a starter `tenzing.yaml`, `settings.json` and `SYSTEM_PROMPT.md` into that per-user directory (embedded in the binary, so there is no install step); existing files are never overwritten, so re-running only fills in gaps.

Located via `--config <path>` > `TENZING_CONFIG` > `./tenzing.yaml` > `<user config dir>/tenzing/tenzing.yaml` (for settings shared across projects). The user config dir is Go's `os.UserConfigDir()`: `~/Library/Application Support/` on macOS, `$XDG_CONFIG_HOME/` (default `~/.config/`) elsewhere — the same `tenzing/` directory that holds `settings.json`, `trust.json`, `AGENTS.md` and `sessions/`. A missing file is an error either way: an explicitly-named path reports the path, a missing file at both probed paths points at `tenzing init`.

Path-valued keys (`system_file`, `nexus_config`, `mcp_servers[].command`) resolve **relative to the config file's own directory**, not the cwd — so a global `<user config dir>/tenzing/tenzing.yaml` can say `system_file: SYSTEM.md` and pick up `SYSTEM.md` from beside it, from any working directory. Absolute paths are used as-is, and a bare `mcp_servers[].command` with no path separator (e.g. `npx`) stays a PATH lookup. The equivalent CLI flags (`--system`, `--nexus-config`) stay cwd-relative, like any other shell argument. Per setting, precedence is **CLI flag > env var > tenzing.yaml > default**. Unknown keys are a startup error, so typos fail loudly. `model`, `providers` and `models` are required; everything else is optional:

```yaml
model: main-model                      # required; names a models: entry below
subagent_model: ""
blackboard_model: ""
advisor_model: ""                     # setting it enables the advisor + write-gate
advisor_nudge: 0

max_turn_tokens: 0                     # per-turn budget (input+output); 0 = unlimited
max_iterations: 0
max_wall_clock: "0s"                   # Go duration string

subagent_depth: 1                      # 0 disables spawn_agent
approval_timeout: "120s"
no_permissions: false
dangerously_skip_permissions: false
read_only: false
thinking: false
no_session: false
no_context_files: false

system_file: ""                        # file replacing the system prompt (--system); relative to this file

port: 8080                             # serve mode
nexus_config: nexus.yaml
debug: false

permissions:                           # per-tool overrides of the default policy
  allow: [Read, Write, Edit]           # a name listed here is dropped from the other lists
  ask: []
  deny: []
  ask_origins: ["mcp:"]                # non-empty replaces the default prefixes

mcp_servers:                           # mounted alongside any --mcp-server flags
  - name: fs
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

providers:                             # required: the backends models are served from
  - name: ollama-cloud                 # unique label, referenced by models[].provider
    type: ollama                       # anthropic|ollama|openai_compat; omit for openai_compat
    url: https://ollama.com/           # required for openai_compat, optional for the other two
    api_key: "$OLLAMA_API_KEY"         # optional; empty = no auth. $VAR / ${VAR} expand from the environment
  - name: openrouter                   # no type: most hosted APIs speak the OpenAI protocol
    url: https://openrouter.ai/api/v1
    api_key: "$OPENROUTER_API_KEY"
    extra:                             # openai_compat only; injected into every request body
      provider.sort: throughput

models:                                # required: the models that can be selected
  - name: main-model                   # unique local alias — what model: and --model refer to
    provider: ollama-cloud             # must name an entry in providers:
    model_name: glm-5.3-flash          # the id sent to the provider on the wire
    context_window: 128000             # optional, default 128k
    max_response_tokens: 32768         # optional, default 32k; caps one response
    vision: false
    reasoning_effort: ""               # optional; provider reasoning tier, e.g. low|medium|high (+ max on Ollama)
    cost: {input: 1.0, output: 3.0}    # USD/MTok; cache_read/cache_write default 0.1x/1.25x input
```

### All options

Every key, with its CLI/env equivalent (which override the file). Durations are Go duration strings (`"90s"`, `"5m"`). A **model ref** is the `name` of a `models:` entry, or an inline JSON definition with the model fields below naming a declared provider. `$VAR` and `${VAR}` anywhere in the file are expanded from the environment before parsing; a reference to an unset variable is left as written.

| Key | Type | Default | Overridden by | Description |
|---|---|---|---|---|
| `model` | model ref | **required** | `--model` | Main model, and the fallback for `subagent_model`/`blackboard_model`. No model at all is a startup error. |
| `subagent_model` | model ref | main model | `--subagent-model` | Model for `spawn_agent` subagents. |
| `blackboard_model` | model ref | main model | `--blackboard-model` | Model for blackboard `llm_query`/`llm_batch`. |
| `advisor_model` | model ref | unset | `--advisor-model` | Setting it enables the `advisor` tool and its write-gate (first state-changing tool call per turn requires a prior consult). |
| `advisor_nudge` | int | `0` (off) | `--advisor-nudge` | Iteration from which an unconsulted executor gets a reminder; requires `advisor_model`. |
| `max_turn_tokens` | int | `0` (unlimited) | `--max-turn-tokens` | Per-turn token budget, input+output cumulative. Distinct from a model's `max_response_tokens`, which caps a single response. |
| `max_iterations` | int | `0` (unlimited) | `--max-iterations` | Per-turn iteration budget. |
| `max_wall_clock` | duration | `"0s"` (unlimited) | `--max-wall-clock` | Per-turn wall-clock budget. |
| `subagent_depth` | int | `1` | `--subagent-depth` | Subagent nesting depth; `0` disables `spawn_agent`. |
| `approval_timeout` | duration | serve `120s` / print `0s` | `--approval-timeout` | Tool-approval wait; `0` denies immediately. |
| `no_permissions` | bool | `false` | `--no-permissions` | Disable permission gating entirely. |
| `dangerously_skip_permissions` | bool | `false` | `--dangerously-skip-permissions` | Auto-approve all approval prompts (sandboxes/pipelines). |
| `read_only` | bool | `false` | `--read-only` | Deny tools not marked read-only; no approval prompts. |
| `thinking` | bool | provider default | `--thinking` | Model reasoning on/off. |
| `no_session` | bool | `false` | `--no-session` | Disable session persistence. |
| `no_context_files` | bool | `false` | `--no-context-files` | Skip AGENTS.md context-file loading. |
| `system_file` | path | unset | `--system` | File whose contents replace the system prompt. Relative paths resolve against the config file's directory. |
| `port` | int | `8080` | `--port`, `SERVER_PORT` | Serve-mode listen port. |
| `nexus_config` | path | `nexus.yaml` | `--nexus-config`, `NEXUS_CONFIG` | Nexus channel config path. |
| `debug` | bool | `false` | `--debug`, `LOG_DEBUG` | Trace-level logging to a fresh log file in `<UserConfigDir>/tenzing/log/`. |
| `permissions` | section | unset | — | Per-tool overrides of the default permission policy; see below. |
| `mcp_servers` | list | `[]` | — (additive with `--mcp-server`) | MCP servers to mount; file entries and flag entries both apply. |
| `providers` | list | **required** | `--provider` (merged by name) | Backends models are served from; see below. |
| `models` | list | **required** | — | Model definitions; see below. |

`permissions` keys. The default policy asks for `bash`, `Write`, `Edit`, `repl`, `spawn_agent` and every `mcp:`-origin tool, and allows everything else. Each list here is merged into that default, and a tool named in one list is removed from the other two — so `allow: [Read, Write, Edit]` stops those prompts while `bash` and MCP tools keep asking. Names match case-insensitively. Precedence at call time is Deny > Ask > Allow > `ask_origins`.

| Key | Type | Description |
|---|---|---|
| `allow` | list | Tools that run without approval. |
| `ask` | list | Tools that require approval. |
| `deny` | list | Tools that are always blocked. |
| `ask_origins` | list | Mount-origin prefixes whose unlisted tools require approval. Non-empty replaces the default `["mcp:"]`. |

`mcp_servers` entries:

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Server name (tool namespace). |
| `command` | string | yes | Executable to launch. |
| `args` | string list | no | Arguments for the command. |

`providers[]` fields. A provider is one backend: a wire protocol plus the endpoint and key to reach it. `name` is a free label, so declaring the same `type` twice lets one config reach a local box and a hosted endpoint. `--provider '{...}'` is the same object as JSON, repeatable, and replaces a file entry of the same name outright (it does not patch it, so restate every field you need).

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Unique label, referenced by `models[].provider`. Also how the backend identifies itself in logs and errors. |
| `type` | string | `openai_compat` | Wire protocol: `anthropic`, `ollama`, or `openai_compat`. Most hosted APIs speak the OpenAI protocol, so this is usually omitted — what distinguishes one such backend from another is its `url`, not a vendor name. An unrecognized value is a startup error; only an absent one defaults. |
| `url` | URL | required for `openai_compat` | Endpoint. Required for `openai_compat`, which has nothing to default to. Optional for `anthropic` (defaults to `https://api.anthropic.com`) and `ollama` (defaults to **`https://ollama.com/`, the cloud endpoint** — set it explicitly to `http://localhost:11434` for a local daemon). |
| `api_key` | string | unset | Omitted or empty means no auth (a local Ollama). Reference the environment rather than writing the secret: `api_key: "$OLLAMA_API_KEY"`. |
| `extra` | map | unset | Fields injected into every request body, by dotted path (`provider.sort: throughput`). `openai_compat` only — silently ignored on the other types. Values are unvalidated: a bad key fails at the provider on the first request, not at startup. One key is reserved: `max_completion_tokens: true` renames the token-limit parameter instead of adding a field, which is what current OpenAI models require. |

`models[]` fields (also the schema for inline JSON model refs). Note `name` and `model_name` are different things: `name` is the alias you refer to the model by, `model_name` is the id the provider knows it as.

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Unique local alias; what `model:`, `--model` and the other model keys refer to. |
| `provider` | string | required | Must name an entry in `providers:`. |
| `model_name` | string | required | The id sent to the provider on the wire (e.g. `glm-5.3-flash`). |
| `context_window` | int | `131072` | Context window size in tokens. |
| `max_response_tokens` | int | `32768` | Max output tokens in a single response. Distinct from the top-level `max_turn_tokens`, which bounds a whole turn. |
| `vision` | bool | `false` | Marks the model as accepting image input; image-bearing queries are rejected without it. |
| `reasoning_effort` | string | unset | Provider reasoning tier, sent verbatim: `reasoning_effort` on OpenAI-compatible providers, Ollama's `think` level (`low`/`medium`/`high`/`max`). The provider validates it — a bad value fails the first request, not startup. Anthropic takes a numeric budget instead and logs a warning. Roles pick it up by referencing the entry (`advisor_model: careful-model`). |
| `cost` | map | unset | USD per MTok: `input`, `output`, optional `cache_read` (default 0.1x input), `cache_write` (default 1.25x input). Feeds `GET /stats` cost tracking. Ignored in inline refs. |

Not configurable via the file (per-run controls, flag-only): `-p/--prompt`, `--output-format`, `--list-models`, `--resume`, `-c/--continue`, `--conversation-id`, `--trust`, `--timeout`. The `connect:` section is the file-based equivalent of the `--connect*` flags (see [Connect mode](#connect-mode-control-plane-dial-out)); its precedence is the same flag > env > file chain as everything else.

## Connect mode (control-plane dial-out)

The third run mode: `tenzing` dials a control plane's WebSocket server instead of listening, making it a supervised agent in a fleet. The control plane owns turn submission, steering, approvals, and model selection; each tenzing process streams its harness events back over the same connection.

```bash
# Dial a control plane (bearer token via env; never on the command line in production)
TENZING_CONNECT_TOKEN=... go run ./cmd/app --connect ws://plane.internal:9000/fleet --dangerously-skip-permissions

# Or from the config file — the fleet-native form (see connect: below)
go run ./cmd/app

# Reconnect pacing (optional; default 1s base, doubling to a 30s cap, jittered)
go run ./cmd/app --connect ws://plane:9000/fleet --connect-backoff 2s
```

How it behaves:

- **Agent dials home** — outbound-only connection, so the control plane allocates no ports and agents work behind NAT, containers, and firewalls unchanged. Connection establishment is the readiness signal; a `{"type":"ready",...}` line on stdout additionally marks "startup succeeded, dialing next" for crash-before-dial visibility.
- **A dropped connection cancels the in-flight turn.** The turn is only meaningful while the control plane can observe it — an orphaned turn burning tokens with nobody watching is worse than a cancelled one, and the control plane owns the retry decision. On reconnect the agent reports what happened in `hello.last_turn` (`cancelled_disconnect`) and immediately accepts fresh work; session persistence means a retry resumes rather than starting over.
- **One turn at a time, follow-ups FIFO.** A `query` arriving mid-turn queues; `cancel` stops the running turn and flushes the queue (serve-mode parity: cancelling wants the agent to stop, not to watch the next query start).
- **Approvals are push.** A mutating tool call needing approval sends `approval_request` on the socket; the plane answers `approve` on the same connection. An `approve` carrying a `glob` ("allow always") adds a bash allow rule **in memory only** — grants die with the process, never leaking into later runs — unless `connect.ephemeral_grants: false`, which persists them to settings.json like serve mode. For unattended fleets, prefer `dangerously_skip_permissions: true` (sandboxed) or `read_only: true` — the unattended default denies mutating tools after the approval timeout.
- **Graceful shutdown.** A `shutdown` command cancels the running turn (its `result` reports `cancelled`), answers `shutdown_ack`, closes the socket, flushes session persistence, and exits 0. SIGTERM does the same without the ack. Neither reconnects. `hello.conversation_id` tells the plane which id to `--resume` on a relaunch, and each `result` carries `files_touched` — the paths of successful Read/Edit/Write calls — for the plane's judge.
- **Lossless backpressure.** A slow control plane stalls the agent's event pump rather than dropping events; nothing is lost while the connection lives. Disconnection is the only thing that discards a turn's event backlog — by design, since the turn was cancelled.

The normative protocol spec is `docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md` (versioned; currently protocol `"1"`): message tables both directions, correlation rules, the handshake, reconnect semantics, auth, and versioning. The control plane is external — build it against that document.

`connect:` section in tenzing.yaml:

```yaml
connect:
  url: ws://plane.internal:9000/fleet   # sets connect mode (same as --connect)
  token: "$TENZING_CONNECT_TOKEN"       # bearer token on the upgrade request
  backoff: "1s"                         # reconnect delay base; doubles to a 30s cap
```

| Key | Type | Default | Overridden by | Description |
|---|---|---|---|---|
| `connect.url` | URL | unset | `--connect`, `TENZING_CONNECT` | The plane's `ws://`/`wss://` endpoint; setting it puts the process in connect mode. |
| `connect.token` | string | unset | `--connect-token`, `TENZING_CONNECT_TOKEN` | Bearer token on the upgrade request; `$VAR` expands from the environment. |
| `connect.backoff` | duration | `"1s"` | `--connect-backoff` | Reconnect delay base; full jitter, doubling to a 30s cap. |
| `connect.ephemeral_grants` | bool | `true` | `--connect-ephemeral-grants`, `TENZING_CONNECT_EPHEMERAL_GRANTS` | Keep runtime-approved bash globs in memory only; `false` persists them to settings.json. |

## HTTP API (serve mode)

`/` chat UI · `GET /events` SSE stream · `GET /debug` log SSE. JSON endpoints:

| Endpoint | Purpose |
|----------|---------|
| `POST /query` | Start a turn (`query` + optional `images[]`); returns `started` or, when busy, `queued` — follow-ups run in order |
| `POST /cancel` | Cancel the running turn and drop queued follow-ups |
| `POST /steer` | Inject a user message into the running turn at the next tool boundary |
| `POST /approve` | Answer a pending tool-approval request |
| `GET /state` | `state`/`loop_state`/`queued`/`conversation_id`/`model`/`vision`/`tools` |
| `GET /sessions`, `DELETE /sessions/{id}`, `PATCH /sessions/{id}` | List / delete / rename recorded sessions |
| `GET /messages` | Conversation history of the active session |
| `POST /compact` | Force context compression (optional `instructions`) |
| `POST /thinking`, `POST /model`, `GET /models` | Toggle reasoning, switch model, list resolvable refs |
| `GET /stats` | Token/cost totals per model |
| `GET /trust`, `POST /trust` | Read / persist the trust decision for the server's cwd |
| `GET /info` | Registered tool count |

## Testing

```bash
go test ./...           # unit tests
go test -race ./...     # race detector
go vet ./...            # static analysis
```

## Project Layout

```
cmd/
  app/                  Entry point — cobra CLI with three modes: HTTP/SSE server
                        with embedded chat UI (default), one-shot print mode (-p),
                        or control-plane connect mode (--connect)

internal/
  core/                 Invariant domain: types, FSM, events, loop, all ports, the Agent
                        contract, tool authoring contract (core/tooldef); imports nothing
                        from internal/
  adapters/             Port implementations (import core only)
    agent/              core.Agent: stateless ModelPort-side brain
    contextstore/       ContextPort: history, pairing, compression (+ compressor/)
    eventbus/           core.Emitter implementation + typed Hooks dispatcher
    toolport/           ToolPort: Composite/Wrap + the native tool Registry
  features/             core.Extension implementations (import core only), each with an
                        ext.go registration: advisor, blackboard, budgets, builtins,
                        mcp, permissions, prompts, reminders, skills, todo
  harness/              Composition root: wiring, config, memory persistence
    runner/             AgentRunner facade over core.Loop
    subagent/           Subagent spawning — a child composition root
    prompttmpl/         Slash-command prompt templates ($1-style expansion)
    session/            Session persistence (JSONL store, persister, list/load)
  app/
    wire/               Versioned JSONL wire contract (event envelopes for json output / SSE)
    nexus/              Input channel monitoring (file-tail/command/webhook → agent wake-ups)
      tools/            Channel tools (list_channels, read_channel, search_channel)
    modelregistry/      Model registry + LLM client factory (tenzing.yaml providers:/models:)
    wsclient/           Control-plane WebSocket client (agent side of connect mode)

docs/                   Reference summaries and API docs
pkg/
  common/               Canonical types: LLM, Model, ModelDefinition, chat types, errors
  models/               Standard model catalog (convenience; any common.Model works)
  providers/
    protocols/          Protocol clients: anthropic, ollama, openai_compat
      ratelimit/        Shared limiting plumbing (TokenBucket, Semaphore, RetryBackoff)
  tenzing/              Public API facade (aliases over internal/harness)
```

## Docs

- `SYSTEM_ARCHITECTURE.md` — full system design
- `AGENTS.md` — conventions for contributing (tools, providers, skills, testing)
- `CLAUDE.md` — AI agent working guidelines
- `docs/http-api.md` — HTTP API reference
