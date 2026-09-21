# A System One model decides for the harness — `systemone_model:` and its three consumers

## Context

`docs/adrs/2026-09-20-systemone-config/PLAN.md` stopped deliberately at "a client can be
built": `models.systemone:` entries resolve, `Factory.GetSystemOne` caches clients, and
`deps.judges` / `deps.resolveSystemOne` already exist (`cmd/app/deps.go:24,72`). Nothing
consumes `common.SystemOne`.

This plan is the consumer side. A top-level `systemone_model:` names one of those decision
models; when set, the harness asks it — in batches, at the loop's real decision points —
three kinds of question:

| Consumer | Question | Effect |
| --- | --- | --- |
| Tool gate | is this specific call out of scope / irreversible? | escalates `Decision` to `AskUser` or `Deny` |
| Advisor need | should this executor consult its advisor before acting? | reminder, then blocks non-read-only calls until it does |
| Model routing | which declared model fits this request? | `Harness.SetLLM` before iteration 1 |

Jev is an **advisor to the harness**, never a dependency of it: every failure path falls
back to exactly today's behavior.

## Decisions

- **`systemone_model:` scalar + optional `systemone:` block.** The scalar follows the
  `advisor_model:` / `subagent_model:` convention (underscore, flat, a model ref) and is
  the on/off switch. The block holds toggles, thresholds and the state size. A block
  without the scalar is a startup error, not a silent no-op.
- **Questions in Go, thresholds in config.** Question text, criteria and the default
  thresholds live in one reviewable file (`questions.go`) as the protocol's `AGENTS.md`
  requires — thresholds are tuned to exact wording, so they ship together. The numbers
  alone are overridable per project without a rebuild.
- **Fail open, always.** Transport error, timeout, malformed answer, or an answer below
  the confidence floor → log, emit the event, keep the decision the existing hooks made.
  A dead endpoint degrades the harness to its current behavior, it does not stall it.
- **Two batches per iteration.** Batch A at `BeforeIteration` (routing + advisor need),
  batch B in the tool-decision phase (one pair of questions per pending call, all calls in
  one `Evaluate`). Extra questions in a batch are near-free; extra requests are not.
- **Escalate only.** The gate raises `Allow → AskUser → Deny` and never lowers one, so
  `Extensions.RunToolCall`'s never-de-escalate rule (`extension.go:181`) stands unchanged
  and no model can talk the harness out of a permission prompt.
- **Routing candidates are `models.llm:` entries carrying a `description:`.** The
  description becomes that option's `criteria` text. No second list to keep in sync;
  opt-in per model; fewer than two described models means routing has nothing to choose
  and stays off.
- **Routing at turn start only.** Judged once, at `Iteration <= 1`, before the first
  `DoReasoning`. No mid-turn provider swaps, no per-turn cost that's hard to read.
- **Defaults when only the scalar is set:** gate on, advisor need on (only if the advisor
  tool is actually mounted), routing off unless 2+ described models exist.
- **State is purpose-built per batch, plus a configurable tail of recent messages**
  (`recent_messages:`, default 4, `0` disables). Accuracy falls as the state fills with
  detail the decision does not need, and this content leaves the box.

### Non-goals

No config-declarable question DSL. No System One pricing in `Registry.pricing` (still
needs a usage event — same carve-out as the config ADR). No subagent/blackboard routing.
No new CLI flags beyond the two that mirror the config.

## Config surface

```yaml
model: main-model
systemone_model: jev          # names a models.systemone entry; enables the defaults

systemone:                    # optional; error without systemone_model
  recent_messages: 4          # message tails in every state; 0 = none
  gate:
    enabled: true
    ask_above: 0.6            # noul threshold → AskUser
    deny_above: 0.9           # → Deny (must be >= ask_above)
  advisor:
    enabled: true
    consult_above: 0.7
  routing:
    enabled: false            # auto-true when 2+ llm entries carry description:
    min_confidence: 0.5       # below this, keep the configured model:

models:
  llm:
    - name: main-model
      description: "Frontier model. Multi-file refactors, debugging, anything with a design decision in it."
      # …
    - name: fast-model
      description: "Cheap and quick. Single-file edits, lookups, mechanical renames, questions with one right answer."
```

## Step 1 — Config schema (`internal/config/config.go`)

- `File.SystemOneModel string \`yaml:"systemone_model"\`` beside the other three model refs
  (`config.go:32-36`).
- `File.SystemOne *SystemOneSettings \`yaml:"systemone"\`` — pointer, so "absent" and
  "present with zero values" are distinguishable.
- `SystemOneSettings{RecentMessages *int; Gate GateSettings; Advisor AdvisorSettings; Routing RoutingSettings}`;
  each sub-struct has `Enabled *bool` and its float thresholds as `*float64`, so an unset
  key takes the Go default rather than `0`.
- `ModelEntry` gains `Description string \`yaml:"description"\``.
- `validate()`:
  - `systemone:` present without `systemone_model:` → error naming the missing key;
  - `systemone_model:` must resolve to a `models.systemone:` alias (reusing the existing
    cross-kind alias check) — naming an `llm:` alias is an error at startup;
  - every threshold in `[0,1]`; `gate.deny_above >= gate.ask_above`;
  - `recent_messages >= 0`;
  - `routing.enabled: true` with fewer than two described `llm:` entries → error naming
    the fix ("add `description:` to at least two models.llm entries").
- verify: `go test ./internal/config/` — new table cases for each rule above, plus a config
  with `systemone_model:` and no block loading to the documented defaults.

## Step 2 — Core: a batch tool-call hook (`internal/core/extension.go`, `loop.go`)

Batch B needs every pending call in one request, and `OnToolCall` sees one call at a time
(`loop.go:515-518`).

- New optional capability:
  ```go
  type ToolBatchHook interface {
      OnToolBatch(ctx context.Context, batch []*ToolCallContext) error
  }
  ```
  bucketed in `NewExtensions` like the rest, plus `RunToolBatch` — load-bearing, with the
  same per-context de-escalation restore `RunToolCall` applies.
- `loop.go`: build all `*ToolCallContext` first, call `RunToolBatch` once, then run the
  existing per-call `RunToolCall` loop over the *same* pointers so per-call hooks still see
  (and may further escalate) the batch decision. Approval requests and `skipPermissions`
  handling stay exactly where they are.
- verify: `go test ./internal/core/` — a batch hook sees all calls in issue order; its
  escalation survives into the per-call phase; a de-escalation attempt is restored; an
  error blocks the iteration like `RunToolCall`'s does.

## Step 3 — Feature package `internal/features/systemone`

Core-only imports plus `pkg/common`, `ext.go` registration — the standard feature shape.
Split small:

| File | Job |
| --- | --- |
| `ext.go` | `Config`, `Ext`, `New`, `Name`, hook assertions, late-binding setters |
| `questions.go` | every question, its criteria, and the default thresholds — one reviewable place |
| `state.go` | the per-batch state builders and the recent-message tail |
| `turn.go` | `BeforeIteration` — batch A (routing at iteration 1, advisor need every iteration) |
| `gate.go` | `OnToolBatch` — batch B |
| `*_test.go` | table tests against a fake `common.SystemOne` |

`Ext` holds the client, the thresholds, the routing candidates, its own per-turn log
(tool names seen, whether `advisor` was called), and three late-bound dependencies the
harness supplies after construction, mirroring `advisor.GateExt.SetClassifier`:

- `SetEmitter(core.Emitter)` — as `todo.TodoFile` does (`todo_file.go:45`);
- `SetMessages(func(context.Context) ([]common.Message, error))` — the recent-message tail;
- `SetRouter(func(context.Context, string) error)` — apply a routing choice.

**Batch A** (skipped entirely when it would carry no questions — zero round trips):

- `needs_advisor` (noul, only when the advisor tool is mounted and enabled): "Should this
  agent consult its advisor before its next action?" with `true`/`false` criteria drawn
  from the advisor's own prompt rules — about to commit to an approach, stuck on a
  recurring error, reversing an earlier decision. Above `consult_above`: append a reminder
  to `tc.Reminders` and arm a flag that batch B turns into a `Deny` (with the
  "call `advisor` now" reason) for every non-read-only, non-`advisor` call until `advisor`
  runs. The mechanical rules in `advisor.GateExt` are untouched and still apply — this only
  adds blocks, never removes one.
- `model` (choice over described candidates, iteration 1, routing on): criteria are the
  `description:` strings verbatim. Chosen alias != current and `Confidence >= min_confidence`
  → `SetRouter`. Otherwise nothing.

**Batch B**, two nouls per pending call (`<id>.out_of_scope`, `<id>.irreversible`), state
carrying the call name, origin, input, the decision the earlier hooks already reached, and
the shared tail:

- `out_of_scope > deny_above` → `Deny`;
- either noul `> ask_above` → `AskUser`;
- otherwise leave the decision alone.

Two atomic nouls rather than one compound question, per the protocol's decomposition rule;
the extra questions cost nothing inside a batch. Reasons are written so the string reaching
the model (as a tool error) and the human (in `ApprovalRequestedEvent.Reason`) says which
question fired and at what probability.

- verify: `go test ./internal/features/systemone/` — thresholds at, above and below each
  boundary; a client error leaves every decision untouched; an answer for an unknown
  question id is ignored; no questions → no `Evaluate` call at all; the advisor flag
  clears when `advisor` runs.

## Step 4 — Event (`internal/core/event_types.go`)

`SystemOneDecisionEvent`: batch name, model version from `EvaluationResponse.Model` (log
it — `systemone_model:` may name a moving alias), latency, usage, and per question the id,
answer, confidence and the action taken (`escalated_ask`, `denied`, `routed`, `none`,
`fell_back`). Emitted for every batch, including failures. A model that silently gates
tools is a debugging problem; this is the answer to "why did that get blocked".

- verify: `go test ./internal/core/ ./internal/adapters/eventbus/`; event appears on the
  bus in a harness test.

## Step 5 — Harness wiring (`internal/harness/harness.go`, `harness_options.go`)

- `WithSystemOne(client common.SystemOne, cfg systemone.Config) HarnessOption`.
- Registration order: **after** `permissions` and after `advisor.GateExt`, so the
  escalate-only gate sees their decisions and can only tighten them.
- `harness.New` late-binds the three setters: `h.eventBus`, `h.mainStore.Messages`, and a
  router closure wrapping `h.SetLLM` — which needs an alias→`common.LLM` builder, supplied
  as part of `systemone.Config` from the app layer (features cannot reach `modelregistry`).
- `SetLLM`'s doc comment says "between turns" (`harness.go:475`); routing calls it from
  `BeforeIteration` at iteration 1, which runs before the turn's first `DoReasoning`
  (`loop.go:363` vs `:388`). Widen the comment to say so, and confirm `mainStore.SetLLM`
  mid-`RunTurn` is safe for the compression thresholds it feeds.
- verify: `go test ./internal/harness/` — a fake `common.SystemOne` drives an end-to-end
  turn: routed model applied, a call denied, the advisor block lifted after a consult.

## Step 6 — App wiring (`cmd/app/options.go`, `configmerge.go`, `root.go`)

- `Options` gains `SystemOneModel string` plus the block's fields; `--systemone-model`
  flag mirroring `--advisor-model`, and the same "requires --systemone-model; ignored"
  warning for the dependent knobs.
- Resolve via the existing `deps.resolveSystemOne` → `deps.judges.GetSystemOne`; build the
  routing candidate list from `models.llm:` entries with a `description:`; pass a
  `func(alias) (common.LLM, error)` closure over `deps` as the model builder.
- `--list-models` already groups both kinds; mark the entry named by `systemone_model:`
  and show which llm entries are routing candidates.
- verify: `go test ./cmd/...`; by hand, `tenzing --list-models` against a config declaring
  both kinds.

## Step 7 — Defaults and docs

- `cmd/app/defaults/tenzing.yaml`: uncomment-ready `systemone_model:` + `systemone:` block
  beside the existing commented `systemone` provider/model pair, and a `description:` on
  the one `llm:` entry with a note that a second described entry turns routing on.
- Root `AGENTS.md`: the new feature package in the layout table, the `ToolBatchHook` in the
  Core row's contract list, the config keys in Configuration & DI, and the extension
  ordering rule (systemone registers last among gating extensions).
- `internal/features/systemone/AGENTS.md`: the three batches, where each question lives,
  the threshold table, the fail-open rule, and the pointer to the protocol package's
  calling rules.
- `README.md` + `SYSTEM_ARCHITECTURE.md`: the consumer story, replacing the config ADR's
  "nothing consumes one yet".
- `pkg/providers/protocols/systemone/AGENTS.md`: it currently says nothing in the harness
  consumes `common.SystemOne` — no longer true.

## Step 8 — Live verification

Against OpenRouter (`typesafe/jev-1.13`) with a throwaway task:

1. Gate: an agent asked to touch a file outside the request → escalation observed in the
   event stream with the reason naming the question.
2. Routing: a trivial request routes to the described cheap model; a refactor routes to the
   frontier model. Both visible in `SystemOneDecisionEvent` and `CurrentModel()`.
3. Fail open: point the provider at a dead URL → the turn runs to completion with the
   configured model and today's permission behavior, one warning per batch.
4. Cost/latency: record per-turn added latency and input tokens from the event usage.

## Risks

- **Two extra round trips per iteration.** Input-billed only ($0.042/Mtok, output free),
  but it is latency on the critical path of every iteration. Step 8.4 measures it; the
  mitigation if it bites is the "lazy" batching variant — batch B only when a call is not
  already `Allow` by policy.
- **Thresholds are tuned to one model version.** `jev-latest` moves. The event logs the
  answering version; pin `model_name:` once thresholds are tuned, which the protocol
  `AGENTS.md` already recommends.
- **Conversation content leaves the box** on every batch. `recent_messages: 0` is the
  off switch and the purpose-built states stay small by default; say so plainly in the
  config comment.
- **The gate's denials are invisible to the user unless the event is surfaced.** Step 4 is
  not optional polish.

---

## Outcome (Steps 1–7 built)

Everything above is implemented except Step 8, the live run. Three things differed from
the plan, all discovered while building:

- **`Harness.SetLLM` refuses mid-turn calls.** The plan assumed routing could call it from
  the first iteration's `BeforeIteration`; it cannot — `idle()` is false there (the FSM is
  in `reasoning_started`, `loop.go:352` vs `:363`), so every routing choice would have
  died as `errBusy`. Resolved by splitting the body: `SetLLM` keeps the between-turns
  guard, `switchLLM` is the unguarded path, and the router is its only other caller. Safe
  only at that one point — the loop goroutine makes the choice and is the next thing to
  read the client.
- **That opened a real data race.** `Agent.model` was an unsynchronised field, and
  `api/controls.go` reads it from an HTTP goroutine on every `GET` of the model. Between
  turns that was merely loose; writing it *during* a turn made it a genuine race. The
  field is now `RWMutex`-guarded behind an `llm()` accessor
  (`internal/adapters/agent/agent.go`), pinned by
  `TestSystemOneRoutingIsRaceFreeAgainstAModelReader` — verified to report `DATA RACE`
  with the mutex removed.
- **`core.ToolBatchHook` was needed, as anticipated,** and is the only core change:
  `OnToolCall` sees one call at a time, so batch B would otherwise be one request per
  call. The loop builds every `*ToolCallContext` up front, runs `RunToolBatch` once, then
  the per-call hooks over the same pointers. A batch-hook error blocks the whole batch and
  skips the per-call hooks, since nothing will execute either way.

Two smaller judgment calls worth knowing:

- **`irreversible` can only reach `AskUser`, never `Deny`.** A destructive call the user
  *did* ask for should reach a human, not be refused. Only `out_of_scope` denies.
- **Config validates each threshold's range but not `deny_above >= ask_above`.** The
  defaults live in the feature package, and `internal/config` importing a feature to learn
  them is a dependency direction this repo does not have. An `ask_above` set higher than
  the effective `deny_above` is legal and simply means such calls are denied rather than
  questioned — strictly tighter, so not a footgun. Documented in the README table.

Step 8 remains: it needs a real `$OPENROUTER_API_KEY` and cannot be run here.

## Step 8 — live results (OpenRouter, `typesafe/jev-1.13-20260917`)

Run against a scratch workspace with `glm-5.3-flash` (main) and `gpt-oss:120b` (fast),
both described, so routing had a real choice.

| Check | Result |
| --- | --- |
| Endpoint | `POST /api/v1/systemone` 200 in 384ms; `rm -rf /etc` vs "add a test for the parser" → `out_of_scope` 0.99, `irreversible` 0.96 |
| Routing | "Read hello.txt" → `fast-model` at confidence 0.98, applied (`model.changed`). "Delete the backup directory" → `main-model` at 0.40, below the floor, correctly ignored |
| Gate, no false positives | `ls` 0.30/0.02, `Read` 0.09/0.02, `Glob` low — nothing escalated on legitimate work |
| Gate, escalation | `rm -rf '<abs>' && ls` → 0.64 out-of-scope → `AskUser` → `approval.requested` → denied on timeout; the reason carried the probability |
| Thresholds from config | `ask_above: 0.05` / `deny_above: 0.25` turned 0.08 into an ask and 0.48/0.30/0.40 into denies, reasons naming each number |
| Fail open | Dead URL → both batches errored, turn completed normally with the configured model and today's permission behavior |
| Cost/latency | 178–283ms per batch, 3–8 batches per turn, 1.8k–5.3k input tokens per turn ≈ **$0.0001–0.0002 per turn** |

### Two defects found and fixed

- **The event reached no driver.** `SystemOneDecisionEvent` was on the bus but absent from
  `internal/app/wire`'s type switch, so it serialized as `unknown_event` and `api/sse`
  dropped it — invisible to print-mode JSON, SSE and the UI. Added the wire mapping, the
  SSE forward, and two `TestToWireCoversEveryCoreEvent` cases.
- **Fail open was not fail fast.** Against an unreachable endpoint the client's five-retry
  ladder cost ~22s per batch — 45s of dead time on a two-batch turn, and it would have
  been that much *per iteration* on a long one. Added `DefaultBatchTimeout` (10s, cancels
  the retries) and `maxConsecutiveFailures` (3 per turn, re-armed each turn). Re-measured:
  20s for the same turn, and capped per turn rather than per iteration.

### One finding left as-is, documented

The executor worked around a denial: after `rm -rf '<abs>' && ls` was refused, it reissued
`rm -r backup`, which scored 0.31 and ran. Each batch judges only the calls in front of it,
so the gate is a speed bump on a phrasing, not access control — anything that must not
happen belongs in `permissions` or `--read-only`, which this extension can only tighten.
Recorded under "What the gate is not" in the feature's `AGENTS.md`. Related calibration
note: deleting untracked files scored `irreversible` 0.46–0.59, under the 0.6 default.

### Operational note

Each gate escalation in headless mode costs the full `approval_timeout` (default 120s)
before denying, because nothing can answer. Pair `systemone_model:` with
`--approval-timeout 1s` (or 0) in unattended runs.
