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
- `openai_compat` — any OpenAI-compatible API: OpenAI, Cerebras, Lightning, OpenRouter, ... (point it with `WithBaseURL`)

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
- **Unified config file** — `tenzing.yaml` holds every durable setting (models incl. the custom-model registry, advisor, subagent, budgets, permissions, MCP servers, serve settings); `--config` / `TENZING_CONFIG` pick the file (default `./tenzing.yaml`), precedence CLI flag > env var > file > default; model flags alternatively take an inline JSON definition (see Quick Start)
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

# Pick a model / set budgets
go run ./cmd/app -p "..." --model anthropic/claude-sonnet-4-6 --max-tokens 50000
go run ./cmd/app --list-models

# Inline model definition: every model flag (--model, --subagent-model,
# --blackboard-model, --advisor-model) also accepts stringified JSON with the
# model entry fields; omitted context_window/max_tokens default to 128k/32k
go run ./cmd/app -p "..." \
  --model '{"provider":"openrouter","name":"deepseek/deepseek-v4-flash-0731","context_window":1048576}'

# Point at a custom endpoint with an explicit key (beats env vars and
# tenzing.yaml base_url; "$VAR" injects the key via shell expansion)
go run ./cmd/app -p "..." --base-url https://openrouter.ai/api/v1 --api-key "$OPENROUTER_API_KEY"

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

The CLI resolves provider API keys from the conventional env vars (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `CEREBRAS_API_KEY`, `LIGHTNING_API_KEY`, `OPENROUTER_API_KEY`; Ollama is keyless, `OLLAMA_API_KEY` optional). `--api-key` overrides the env var and `--base-url` overrides tenzing.yaml `base_url` and the provider default endpoints (e.g. `--base-url https://openrouter.ai/api/v1 --api-key "$OPENROUTER_API_KEY"`). Optional env: `TENZING_CONFIG` (tenzing.yaml path, default `./tenzing.yaml`), `TENZING_MODEL` (default model as `provider/name`), `TENZING_PROJECT_TRUST` (`trust` to load project-local config by default).

## Config file (`tenzing.yaml`)

One YAML file for every durable setting. Located via `--config <path>` > `TENZING_CONFIG` > `./tenzing.yaml`; a missing file at the default path is fine, a missing explicitly-named file is an error. Per setting, precedence is **CLI flag > env var > tenzing.yaml > default**. Unknown keys are a startup error, so typos fail loudly. All keys optional:

```yaml
model: openrouter/some-model          # main model; ref or inline {provider,name,...}
subagent_model: ""
blackboard_model: ""
advisor_model: ""                     # setting it enables the advisor + write-gate
advisor_nudge: 0

max_tokens: 0                          # per-turn budgets; 0 = unlimited
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

system_file: ""                        # file replacing the system prompt (--system)
base_url: ""                           # LLM endpoint override
api_key: ""                            # works, but env vars / --api-key are preferred

port: 8080                             # serve mode
nexus_config: nexus.yaml
debug: false

mcp_servers:                           # mounted alongside any --mcp-server flags
  - name: fs
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

models:                                # custom model registry (replaces models.yaml)
  default: ""                          # fallback default; the top-level model: wins
  entries:
    - provider: openrouter             # anthropic|cerebras|lightning|ollama|openai|openrouter
      name: some-model
      context_window: 128000           # optional, default 128k
      max_tokens: 32768                # optional, default 32k
      base_url: ""                     # optional, applies to the whole provider
      vision: false
      cost: {input: 1.0, output: 3.0}  # USD/MTok; cache_read/cache_write default 0.1x/1.25x input
```

### All options

Every key, with its CLI/env equivalent (which override the file). Durations are Go duration strings (`"90s"`, `"5m"`). Model refs are `provider/name` or an inline JSON definition with the entry fields below.

| Key | Type | Default | Overridden by | Description |
|---|---|---|---|---|
| `model` | model ref | `ollama/glm-5.2:cloud` | `--model`, `TENZING_MODEL` | Main model. Wins over `models.default`. |
| `subagent_model` | model ref | main model | `--subagent-model` | Model for `spawn_agent` subagents. |
| `blackboard_model` | model ref | main model | `--blackboard-model` | Model for blackboard `llm_query`/`llm_batch`. |
| `advisor_model` | model ref | unset | `--advisor-model` | Setting it enables the `advisor` tool and its write-gate (first state-changing tool call per turn requires a prior consult). |
| `advisor_nudge` | int | `0` (off) | `--advisor-nudge` | Iteration from which an unconsulted executor gets a reminder; requires `advisor_model`. |
| `max_tokens` | int | `0` (unlimited) | `--max-tokens` | Per-turn token budget (input+output cumulative). |
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
| `system_file` | path | unset | `--system` | File whose contents replace the system prompt. |
| `base_url` | URL | provider default | `--base-url` | LLM endpoint base URL; beats per-provider `models.entries[].base_url`. |
| `api_key` | string | provider env var | `--api-key` | LLM API key. Prefer the provider env vars over a key on disk. |
| `port` | int | `8080` | `--port`, `SERVER_PORT` | Serve-mode listen port. |
| `nexus_config` | path | `nexus.yaml` | `--nexus-config`, `NEXUS_CONFIG` | Nexus channel config path. |
| `debug` | bool | `false` | `--debug`, `LOG_DEBUG` | Trace-level logging to a fresh log file. |
| `mcp_servers` | list | `[]` | — (additive with `--mcp-server`) | MCP servers to mount; file entries and flag entries both apply. |
| `models` | section | empty | — | Custom model registry; see below. |

`mcp_servers` entries:

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Server name (tool namespace). |
| `command` | string | yes | Executable to launch. |
| `args` | string list | no | Arguments for the command. |

`models` section:

| Key | Type | Default | Description |
|---|---|---|---|
| `default` | model ref | unset | Default model when neither `--model`, `TENZING_MODEL`, nor top-level `model:` picks one. |
| `entries` | list | `[]` | Custom model definitions, layered over the compiled-in set (same `provider/name` overrides the builtin). |

`models.entries[]` fields (also the schema for inline JSON model refs):

| Key | Type | Default | Description |
|---|---|---|---|
| `provider` | string | required | One of `anthropic`, `cerebras`, `lightning`, `ollama`, `openai`, `openrouter`. |
| `name` | string | required | Model name as the provider expects it. |
| `context_window` | int | `131072` | Context window size in tokens. |
| `max_tokens` | int | `32768` | Max output tokens per response. |
| `base_url` | URL | provider default | Endpoint override; applies to the whole provider. Ignored in inline refs. |
| `vision` | bool | `false` | Marks the model as accepting image input; image-bearing queries are rejected without it. |
| `cost` | map | unset | USD per MTok: `input`, `output`, optional `cache_read` (default 0.1× input), `cache_write` (default 1.25× input). Feeds `GET /stats` cost tracking. Ignored in inline refs. |

Not configurable via the file (per-run controls, flag-only): `-p/--prompt`, `--output-format`, `--list-models`, `--resume`, `-c/--continue`, `--conversation-id`, `--trust`, `--timeout`.

**Migrating from models.yaml** (no longer read): move its content under the `models:` key, renaming the `models:` list to `entries:`, and delete the `TENZING_MODELS_CONFIG` env var — e.g. `default: x` + `models: [...]` becomes `models: {default: x, entries: [...]}` in `tenzing.yaml`.

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
  app/                  Entry point — cobra CLI: HTTP/SSE server with embedded chat UI
                        by default, or one-shot print mode (`-p`)

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
