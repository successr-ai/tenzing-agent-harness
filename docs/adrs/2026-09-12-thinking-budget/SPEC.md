# SPEC: Cross-Provider Thinking Budget (Opt-In)

Self-contained execution spec — assumes **no prior conversation context**. Line numbers were accurate at time of writing; treat them as anchors and search for the named symbols if they've drifted.

## Ground rules (from repo `CLAUDE.md` / root `AGENTS.md` — read both first)

- Update every `AGENTS.md`/doc statement your change makes untrue **in the same change**. This change touches the config surface and harness options — the root `AGENTS.md` sections on `HarnessOption`s, `cmd/app` flags, and `internal/config` will need edits.
- Do not commit or push — repo owner handles version control.
- No stray build artifacts (compile-check with `go build -o /dev/null ./...`).
- Table-driven Go tests; run `go test ./...` and `go test -race ./...`.
- Surgical changes; match existing style.

## Problem

Thinking models occasionally produce extremely long single reasoning stretches (600s+ observed). The user wants to cap how long the model is *allowed to think*, without disabling thinking entirely (too blunt) and without per-model `reasoning_effort` tuning (annoying, not portable across providers).

Today there is no way to express this. `common.CompletionRequest.ThinkingBudget` exists (`pkg/common/chat.go:212`) and every protocol has a mapping for it, but nothing sets it — no config field, no CLI flag, no harness option. The value is dead code on the request path.

## Background

### Existing machinery (verified in code)

- **Canonical type** — `common.CompletionRequest.ThinkingBudget *int64` (`pkg/common/chat.go:207-212`), documented as "honored alongside Think".
- **Agent adapter** — `internal/adapters/agent/agent.go:242-252` builds the per-iteration request with `MaxTokens: maxTokensStdResponse` (32768) and `Think: a.think`; no budget is set. Agent config (`AgentConfig`, :48-59) carries `Think *bool` from `harness.WithThinking`. `SetThinking` (:117) toggles it at runtime.
- **Anthropic** — native exact mapping. `pkg/providers/protocols/anthropic/anthropic.go:401-408`: `ThinkingBudget` requires `Think=true`; budget is clamped to ≥1024 and **must be < `MaxTokens`** (hard validation error otherwise). Sent as `{"thinking": {"type": "enabled", "budget_tokens": N}}`.
- **openai_compat** — lossy tier mapping. `openai_compat.go:434-435`: a set budget overrides any configured `reasoning_effort` via `reasoningEffortForBudget` (`openai_compat_convert.go:171`, "Lossy by design: the API has no numeric budget"). Tiers: map budget → `low`/`medium`/`high` per the existing helper's thresholds.
- **Ollama** — budget deliberately **ignored** (`ollama_thinking_test.go:17-43` pins that setting it doesn't change the wire body). Reasoning control is `think` levels via `WithReasoningEffort` (`ollama.go:111-114`: "low", "medium", "high", "max"; an explicit `Think: false` beats the level).
- **Model-level config** — tenzing.yaml model entries already carry `reasoning_effort` (`internal/config/config.go:160-164`), flowing into protocol client constructors through `internal/app/modelregistry`'s `Factory`. **The effort is baked into the client, not per-request** — it becomes part of the client cache key (`cmd/app/deps.go` Factory, keyed provider+model+reasoning effort).
- **Per-response cap** — model entries have `max_response_tokens` (config.go:153-155 → `ModelDefinition.MaxTokens`). Anthropic sends it as `max_tokens` (with the budget-must-be-less rule); Ollama as `num_predict` (`ollama.go:568`); compat as `max_tokens`/`max_completion_tokens` (`openai_compat.go:406-424`).

### Why this design (decisions final — do not relitigate)

- **Not "disable thinking"** (`--thinking=false` / `SetThinking(false)`): too blunt; user wants bounded reasoning, not none.
- **Not per-model `reasoning_effort` tuning**: requires editing tenzing.yaml per deployment, is spelled differently per provider (string levels vs numeric), and isn't a single "cap it" knob.
- **Not provider extras**: Anthropic-only for a numeric budget; not portable.
- **Not wall-clock alone**: fully portable (budgets extension terminates the turn) but kills the whole turn, not just the reasoning stretch.
- **The budget is the one knob that maps natively everywhere**: exact on Anthropic, tiered on compat, level-mapped on Ollama. Reuse `ThinkingBudget`; do NOT add a parallel mechanism.
- **Unset budget → behavior byte-identical to today** (nil → nothing set on the wire).

## Design

One new knob: a **thinking budget** (tokens), configured globally for the main agent, plumbed into every `CompletionRequest` the default agent issues.

- Config sources, in precedence order: CLI flag `--thinking-budget N` > tenzing.yaml top-level `thinking_budget:` (NOT per-model-entry — the whole point is one knob above the provider zoo).
- Plumbed: `cmd/app` → `harness.WithThinkingBudget(*int64)` → `AgentConfig.ThinkingBudget *int64` → `CompletionRequest.ThinkingBudget` in `DoReasoning`.
- Per-protocol behavior (already implemented, just unexercised):
  - **Anthropic**: exact. Sent as budget_tokens; validated < MaxTokens.
  - **openai_compat**: tiered via existing `reasoningEffortForBudget`; overrides a model entry's `reasoning_effort` for that request (matches existing code path at openai_compat.go:434).
  - **Ollama**: budget is translated to the closest `think` level (see "Ollama mapping" below) — this is the ONE new protocol-side change, converting the current ignore into a lossy level mapping, mirroring compat's approach.
- Unset (nil) on any protocol → wire body unchanged from today (pinned by existing tests).

### Decisions (nail these down; advisor-confirmed)

1. **Budget vs model-entry `reasoning_effort`**: explicit per-turn budget **wins**, with a one-line `slog.Warn` at startup when both are set for the main model ("thinking_budget overrides reasoning_effort"). No error — the budget is the more specific, more recent intent. On compat/Ollama this is automatic since the budget path replaces the effort; the warning is informational for Anthropic users who set both (there the two compose: budget caps tokens, effort is unused).
2. **Budget vs `max_response_tokens`**: never a hard error at the CLI. If budget ≥ the main model's `MaxTokens`, clamp to `MaxTokens - 1024` (Anthropic's own SDK requires budget < max_tokens; the clamp keeps that satisfiable) and `slog.Warn` with both numbers. Computed at harness construction, not per request.
3. **Budget vs `--thinking=false`**: if `Think` is explicitly false, the budget is **silently inert** (no warning — combining them is not an error, the user may keep the budget set while toggling thinking off at runtime via `/thinking`). If `Think` is unset/true, budget implies thinking is wanted but does NOT set `Think=true` itself — Anthropic rejects budget without think=true, so on Anthropic only, when budget is set and `Think` is nil, set `Think=true` in the agent adapter (documented; mirrors how the SDK option works today).
4. **Runtime change**: add `Harness.SetThinkingBudget(int64)` (between-turns only, same contract as `SetThinking`), emitting a `ThinkingChangedEvent` with the new budget in an additive field... **No** — scope creep. Instead: expose it as a property on the existing model-change path only if trivial. **Decision: skip a runtime setter this PR**; the flag + config file cover the need, and `SetThinking` already exists for the on/off axis. Note it as a follow-up.
5. **Scope of agents**: main agent only, like `WithThinking`. Subagents/blackboard/advisor roles keep default behavior (they get no budget; their prompts already bias short reasoning). Do not plumb into `WithSubagentLLM` etc.
6. **Client cache key**: NOT affected. The budget rides on the `CompletionRequest`, not the client — no Factory key change is needed (this is exactly why the budget is a request field and the effort is a client option). Verified: the compat tier override happens per-request from the request field, not the client config.

### Ollama mapping (new, small)

Reuse compat's tier thresholds so the two lossy providers agree. Read `reasoningEffortForBudget` in `pkg/providers/protocols/openai_compat/openai_compat_convert.go` for the exact cutoffs, then in `ollama.go`'s `think()` (`ollama.go:553`), when `req.ThinkingBudget != nil` and no explicit `Think=false`, map the budget to `"low"`/`"medium"`/`"high"`/`"max"` with the same cutoffs. Update `ollama_thinking_test.go`: the "budget ignored" pin becomes "budget maps to a think level; explicit think:false still wins". Keep the test that a budget with `Think=false` produces the same body as no budget (explicit off beats everything).

## Implementation steps

### Step 1 — Config surface (`internal/config/config.go`)

- Add to `File`: `ThinkingBudget *int \`yaml:"thinking_budget"\`` (top-level, alongside `max_wall_clock` at config.go:43). Pointer for unset-vs-zero.
- No per-model-entry field.

### Step 2 — Harness option (`internal/harness/harness_options.go`)

- `WithThinkingBudget(tokens *int) HarnessOption` — stored on `HarnessConfig`-equivalent options struct; nil = unset. Re-export in `pkg/tenzing` (`tenzing.go`) in the same change (repo rule: new harness options must be re-exported).
- In `harness.New`, pass to the default agent builder: `agent.AgentConfig{ThinkingBudget: o.thinkingBudget, ...}` next to `Think`.
- Apply decision 2's clamp here (compare against the main model's `GetMaxTokens()`); warn + clamp once at construction.

### Step 3 — Agent adapter (`internal/adapters/agent/agent.go`)

- `AgentConfig.ThinkingBudget *int64`; stored on `Agent`.
- In `DoReasoning`'s request construction (:242): set `ThinkingBudget: a.thinkingBudget`.
- Anthropic-only `Think` implication (decision 3): simplest correct placement is a method on the Anthropic client, not the agent — in `anthropic.go` where `req.ThinkingBudget != nil` is already handled (:401), if `req.Think == nil || !*req.Think` and budget set, treat as think-enabled for the wire (send the thinking block). This keeps cross-protocol semantics out of the core adapter. **Update the Anthropic thinking test** to pin this.
- Warn-once at construction if budget set and main-model `MaxTokens` known and budget ≥ it (done in Step 2; agent layer trusts the clamped value).

### Step 4 — CLI (`cmd/app/`)

- Flag: `--thinking-budget` (int, 0 = unset → nil), on root command; add to `cliConfig` (`cmd/app/options.go`), `markSetFlags` not needed (unset-vs-zero handled via pointer after flag parse: `if cfg.ThinkingBudget > 0`).
- `mergeConfigFile`: file value fills the flag-unset field like other scalars.
- `harnessOptions` (`cmd/app/options.go`): map to `tenzing.WithThinkingBudget`.
- Help text: "Cap reasoning tokens per LLM call (exact on Anthropic, tiered/level-mapped on OpenAI-compatible and Ollama; 0 = provider default)".

### Step 5 — Ollama mapping (see above; the only protocol change)

### Step 6 — Tests

- Config: YAML parse + merge precedence (`internal/config/config_test.go`, `cmd/app` merge tests).
- Harness: option flows to `AgentConfig`; clamp behavior (budget ≥ MaxTokens → clamped + warned).
- Agent: request carries the budget when set, absent when nil (table-driven).
- Ollama: budget → level mapping table (mirror the compat tier test shape); explicit `think:false` still beats it.
- Anthropic: budget implies think-enabled on the wire when `Think` nil; existing budget/MaxTokens validation tests still pass.
- Compat: no new test needed — existing `TestOpenAICompat_ThinkingBudgetMapsToReasoningEffort` already pins the path; confirm it exercises the config-set path (it sets the field directly, which is now the same path).

### Step 7 — Docs

- Root `AGENTS.md`: add `WithThinkingBudget` / `--thinking-budget` to the harness-option and CLI flag lists (one line each, in the existing style).
- `docs/adrs/2026-08-11-tenzing-yaml-config/` — check its spec for a config-field table; add `thinking_budget` if one exists.
- `SYSTEM_ARCHITECTURE.md` — only if it enumerates the config surface; grep `reasoning_effort` there to find the spot.

## Explicitly out of scope

- Runtime budget setter (`SetThinkingBudget`) — follow-up if needed.
- Per-model-entry or per-role (subagent/blackboard/advisor) budgets.
- Wall-clock budget (`--max-wall-clock`) already exists; do not touch it, but mention it in the flag help as the complementary whole-turn guard.
- Adaptive/"advisor breaks out of long thinking" behavior — rejected: nothing can run concurrently with an in-flight reasoning stream; the advisor is a between-calls consult, not an interrupt.