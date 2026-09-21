# systemone (feature)

Lets a **System One decision model** — TypeSafe's Jev, through `common.SystemOne` — make three harness decisions. The client and the wire contract live in `pkg/providers/protocols/systemone`; this package is the only thing in the harness that consumes one. Plan: `docs/adrs/2026-09-20-systemone-harness/PLAN.md`.

| Decision | Hook | Effect |
| --- | --- | --- |
| Tool gate | `core.ToolBatchHook` | escalates a call's `Decision` to `AskUser` or `Deny` |
| Advisor need | `core.BeforeIterationHook` + the same batch hook | reminder, then blocks state-changing calls until `advisor` runs |
| Model routing | `core.BeforeIterationHook` (iteration 1) | calls the late-bound router, i.e. `Harness.SetLLM` |

## Two batches, never more

Jev ingests the state once and answers every question in the request in parallel, so extra questions are near-free and extra requests are not. Hence exactly two `Evaluate` calls per iteration, at the loop's two real decision points:

- **Batch A** (`turn.go`, `BeforeIteration`) — `needs_advisor` every iteration, `model` on the first. No questions to ask (both consumers off, or routing already asked) means **no request at all**.
- **Batch B** (`gate.go`, `OnToolBatch`) — two atomic nouls per pending call, every call of the iteration in one request. `core.ToolBatchHook` exists for this: `OnToolCall` sees one call at a time, which would be one request per call.

Calls already at `Deny` are not asked about — nothing an answer could say would change them.

## Rules this package must keep

- **Fail open, always — and fail fast.** A transport error, a missing answer, an unknown alias, a failing router, an unavailable conversation store: every one of them leaves the harness doing exactly what it would have done without this extension. Neither hook ever returns an error — both are load-bearing in the loop, so returning one would let a dead endpoint stall the turn. `evaluate` reports failure as `(zero, false)` and emits the event; callers `return nil`.

  Failing open is not enough on its own: measured against an unreachable endpoint, the protocol client's five-retry ladder took **~22s per batch**, two batches an iteration. So `evaluate` puts `DefaultBatchTimeout` (10s) on every call, which cancels the retries, and `maxConsecutiveFailures` (3) stops calling for the rest of the turn once the endpoint is clearly down. A success resets the count; each new turn re-arms it. Measured after: 20s for a two-batch turn instead of 45s, and capped per turn rather than per iteration.
- **Escalate only.** `escalate` never lowers a `Decision`; core's `RunToolBatch` would restore it anyway. Register this extension **after** `permissions` and `advisor.GateExt` so it sees their decisions and can only tighten them.
- **Questions and thresholds ship together** (`questions.go`). A threshold is calibrated against one exact wording — changing the words invalidates the number. Config may override the numbers; the wording takes a code change.
- **Ask atomically.** Two nouls (`out_of_scope`, `irreversible`) rather than one "is this dangerous": they carry different costs and combine in Go. Same reason the advisor question's criteria list observable situations instead of asking "is it stuck".
- **Filter the state.** Purpose-built per batch, plus at most `RecentMessages` trailing messages, each truncated at `maxMessageChars`. Accuracy falls as the state fills with detail the decision does not need, and this content leaves the machine. Thinking blocks never travel.

## Files

| File | Job |
| --- | --- |
| `ext.go` | `Config`, `Ext`, `New`, the late-binding setters, `evaluate`/`report`/`emit` |
| `questions.go` | every question, its criteria, the default thresholds, the reason strings |
| `state.go` | the per-batch state shapes and the recent-message tail |
| `turn.go` | batch A — advisor need, model routing |
| `gate.go` | batch B — the tool gate, plus the advisor block it enforces |

## Late binding

`New` takes the client; four dependencies arrive afterwards, because the composite `ToolPort`, the context store and the LLM switch are all built after the extension set — the ordering `advisor.GateExt.SetClassifier` already solves:

| Setter | Bound to | Unset behaviour |
| --- | --- | --- |
| `SetEmitter` | the harness event bus | events dropped |
| `SetMessages` | the context store | no recent-message tail |
| `SetRouter` | `Harness.SetLLM` + the model registry | routing decided, never applied |
| `SetClassifier` | `Composite.ReadOnly` | everything counts as mutating |

`New(Config{})` with a nil `Client` returns a nil `*Ext`, so a caller can pass the result to `harness.WithExtension` without branching.

## What the gate is not

**A denied call can come back in a cheaper-scoring form.** Observed live: `rm -rf '<abs path>' && ls` scored 0.64 out-of-scope and was escalated to an approval, which headless mode denied; the executor then reissued it as `rm -r backup`, which scored 0.31, cleared both thresholds, and ran. Questions in a batch are independent and each batch judges only the calls in front of it, so the gate is a **speed bump on a specific phrasing, not access control**. Anything that must not happen belongs in `permissions` (name and command rules, which this extension cannot loosen) or `--read-only`. The advisor block is the one sticky piece of state here; the gate deliberately has none.

Also measured: deleting untracked files scored `irreversible` 0.46–0.59, just under the 0.6 ask threshold, while `rm -rf /etc` against an unrelated request scored 0.96. Tune `gate.ask_above` down if untracked deletion should prompt.

## Events

Every batch emits `core.SystemOneDecisionEvent` (`systemone.decision`), failures included — a decision model that silently gates tools or swaps the LLM is otherwise undebuggable. It carries the batch name, the **version that answered** (`jev-latest` moves; log what actually ran), duration, input tokens, and one `SystemOneDecision` per question with its answer, confidence, the action taken (`none`/`ask`/`deny`/`remind`/`route`) and its target. A non-empty `Error` means the batch changed nothing.

## Wiring

`harness.WithSystemOne(cfg, resolveModel)` registers it (re-exported as `tenzing.WithSystemOne`, with the config types aliased beside it). `harness.New` binds all four setters, and `resolveModel` — an alias→`common.LLM` func from the app layer, since the harness cannot resolve aliases itself — becomes the router. A `Config` with no client registers nothing, so the zero value is safe to pass unconditionally.

**Routing switches the model mid-turn, deliberately.** The choice is made in the first iteration's `BeforeIteration`, at which point the loop FSM has left the idle states and `Harness.SetLLM` would refuse it (`errBusy`). The router therefore calls `Harness.switchLLM`, the same body without the between-turns guard. That is safe only at that one moment: the loop goroutine makes the choice and is the next thing to read the client, on the line after the hooks return. `Agent.model` is `RWMutex`-guarded (`internal/adapters/agent`) because a driver may be reading the model name over HTTP at the same time — `TestSystemOneRoutingIsRaceFreeAgainstAModelReader` pins it.

`cmd/app/systemone.go` builds all of this from the config: the client through `deps.judges`, the candidates from `models.llm:` entries carrying a `description:`, and the resolver from the model registry. Two defaults live there rather than here, because only that layer can tell "unset" from "zero": `RecentMessages` (0 means *send none*, so `New` cannot default it — use `DefaultRecentMessages`) and whether the advisor consumer has an `advisor` tool to consult at all.
