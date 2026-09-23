# systemone (feature)

Lets a **System One decision model** — TypeSafe's Jev, through `common.SystemOne` — make three harness decisions. The client and the wire contract live in `pkg/providers/protocols/systemone`; this package is the only thing in the harness that consumes one. Plan: `docs/adrs/2026-09-20-systemone-harness/PLAN.md`.

| Decision | Hook | Effect |
| --- | --- | --- |
| Tool gate | `core.ToolBatchHook` | escalates a call to `AskUser` when it leaves the working directory, may destroy unrecoverable work, or may touch secrets |
| Advisor need | `core.BeforeIterationHook` + the same batch hook | reminder, then blocks state-changing calls until `advisor` runs |
| Model routing | `core.BeforeIterationHook` (iteration 1) | calls the late-bound router, i.e. `Harness.SetLLM` |

## Two batches, never more

Jev ingests the state once and answers every question in the request in parallel, so extra questions are near-free and extra requests are not. Hence exactly two `Evaluate` calls per iteration, at the loop's two real decision points:

- **Batch A** (`turn.go`, `BeforeIteration`) — `needs_advisor` every iteration, `model` on the first. No questions to ask (both consumers off, or routing already asked) means **no request at all**.
- **Batch B** (`gate.go`, `OnToolBatch`) — one rule and two atomic nouls per pending call, every call of the iteration in one request. `core.ToolBatchHook` exists for this: `OnToolCall` sees one call at a time, which would be one request per call. The rule — a path outside the working directory, temp exempt — is a computed fact and asks nothing; it applies even when the model is down.

Calls already at `Deny` are not asked about — nothing an answer could say would change them. In the live loop that is only this extension's own advisor block: `core.Loop` runs batch hooks **before** every per-call hook, so permissions and the advisor gate have not decided yet, and the gate asks about calls they will go on to deny.

## Rules this package must keep

- **Fail open, always — and fail fast.** A transport error, a missing answer, an unknown alias, a failing router, an unavailable conversation store: every one of them leaves the harness doing exactly what it would have done without this extension. Neither hook ever returns an error — both are load-bearing in the loop, so returning one would let a dead endpoint stall the turn. `evaluate` reports failure as `(zero, false)` and emits the event; callers `return nil`.

  Failing open is not enough on its own: measured against an unreachable endpoint, the protocol client's five-retry ladder took **~22s per batch**, two batches an iteration. So `evaluate` puts `DefaultBatchTimeout` (10s) on every call, which cancels the retries, and `maxConsecutiveFailures` (3) stops calling for the rest of the turn once the endpoint is clearly down. A success resets the count; each new turn re-arms it. Measured after: 20s for a two-batch turn instead of 45s, and capped per turn rather than per iteration.
- **Escalate only, and only to `AskUser`.** `escalate` never lowers a `Decision`; core's `RunToolBatch` would restore it anyway. The gate never denies on the model's word: each of its three concerns is something a human should see, not something a probability should refuse. Registration order does not matter for the gate: it is a batch hook, and the loop runs every batch hook before any per-call hook, so it never sees what `permissions` or `advisor.GateExt` decide. It does not need to — the loop keeps the strictest decision any hook reaches, so an `AskUser` here survives a later `Allow`, and a later `Deny` survives an `AskUser` here.
- **Questions and thresholds ship together** (`questions.go`). A threshold is calibrated against one exact wording — changing the words invalidates the number. Config may override the numbers; the wording takes a code change, and **`evals/systemone` is how you find out whether that change helped**: 32 labelled tool calls, ~4s and $0.0004 per run, reporting each question's separation from the class it is responsible for. `IrreversibleQuestion`, `SecretsQuestion`, `AdvisorQuestion` and `TurnState` are exported for it so the measurement runs against the shipping wording and state. `go run ./evals/systemone -advisor` measures the advisor-need question the same way, against 16 labelled turn moments: separation +0.13 to +0.14 with the current wording and state, measured window (0.43, 0.56]; `consult_above` defaults to 0.5, inside it. See `evals/AGENTS.md`. Measured today, 32 cases: `irreversible` holds recoverable calls at ≤0.23 (median 0.04) and destructive ones at ≥0.61 — separation +0.38; `secrets` holds clean calls at ≤0.62 (median 0.07) and secret-touching ones at ≥0.81 — separation +0.19. The outward calls (deploy, push) are what lift the clean tail on `secrets`: the model associates "sends" with "might send secrets".
- **Ask atomically, and only what a fact cannot settle.** Two nouls (`irreversible`, `secrets`) plus one rule (outside the working directory) rather than one "is this dangerous": the concerns carry different costs and combine in Go, and the one that is computable is computed. The gate does **not** ask whether a call was requested. It did once, and the model read it literally — `echo ok` after a delete request scored 0.89 "unrequested", a helpful adjacent typo fix scored the same as exfiltration — flagging every sensible step the request did not spell out. The harness's concerns are where a call reaches, what it destroys and what it exposes; whether the user named it is not one of them.
- **Compute the facts, ask only the judgments.** Where a call lands is not a matter of opinion, so `paths.go` resolves it — absolute, symlinks followed, `~` expanded, relative against the working directory — and hands the model `paths: [{path, outside_working_directory, in_temp_directory}]`. The temp frame is two roots, `os.TempDir()` and `/tmp` — on macOS the first is a per-user `/var/folders` path, and `/tmp` is where scratch actually goes. Every frame is resolved at construction, because on macOS `/tmp` is a symlink to `/private/tmp` and comparing an unresolved root would report the temp directory as somewhere else. Measured effect on `rm -r backup`, same question, state the only variable: no facts → `irreversible` 0.83; resolving into the temp directory → **0.26**; resolving to `~/.ssh/backup` → `out_of_scope` 0.05 → **0.24**. The model stops reading a string and starts judging a location.

  Extraction is best-effort and says so by omission: native tools declare their paths in the schema (`pathArgs`; `Glob`'s `pattern` too, by tool name, since grep's `pattern` is a regex), `bash` goes through the shell parser — argv words, redirect targets (`> f`, `>> ~/.zshrc`, `< in`) a long flag's attached value (`--target-directory=/x`), plain parameters (`$HOME/.ssh`, `${D}/x`) expanded from the environment the bash tool inherits, and path literals inside the program after a code flag (`python3 -c "open('/etc/passwd')"`, `node -e`; `-c`/`-e`/`-E`/`--eval`/`--command`). A word the gate cannot place is skipped rather than guessed at: a variable unset in this process, one assigned earlier in the same line (`D=/etc; cat $D` resolves `$D` against the inherited environment), `$(…)`, an operator like `${a:-b}`, or a path an interpreter builds at runtime (`os.path.join`). Those are what `permissions` rules and `--read-only` are for. `maxPathFacts` caps the list. The parser arrives through `SetShellSplitter` because it lives under another feature (`permissions/shell`) and a feature may not import one — unbound, bash reports no paths rather than wrong ones.
- **Filter the state.** Purpose-built per batch, each message truncated at `maxMessageChars`. Batch A (advisor need, routing) sends the turn's `request` plus the text of the last `RecentMessages` **assistant** messages, `recent_assistant_messages` — no tool calls, no tool results, and a message that only called tools takes no slot. Committing, reversing, being stuck and declaring done show in what the agent says; a tail of `[called Edit]` lines crowded that out, and the tool log added nothing the words did not. The one tool fact kept is `advisor_consults_this_turn`, which the "it has just consulted" signal needs. Batch B keeps the full tail of the last `RecentMessages` messages of any role, the request pinned first — there the tool call is the subject, and results such as a listing inform the irreversibility judgment. Accuracy falls as the state fills with detail the decision does not need, and this content leaves the machine. Thinking blocks never travel.

## Files

| File | Job |
| --- | --- |
| `ext.go` | `Config`, `Ext`, `New`, the late-binding setters, `evaluate`/`report`/`emit` |
| `questions.go` | every question, its criteria, the default thresholds, the reason strings |
| `state.go` | the per-batch state shapes and the recent-message tail |
| `paths.go` | resolves the filesystem locations a call touches and places them against the working and temp directories |
| `turn.go` | batch A — advisor need, model routing |
| `gate.go` | batch B — the tool gate, plus the advisor block it enforces |

Debug aid: `Config.CaptureFile` (`TENZING_SYSTEMONE_CAPTURE=<path>` from the CLI) appends every batch — state, questions, answers — as one JSON line, for replaying a live judgment against a candidate wording. The file holds conversation content; env-only on purpose.

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

**It is not access control.** Questions in a batch are independent and each batch judges only the calls in front of it; the gate has no memory of what it refused. Anything that must not happen belongs in `permissions` (name and command rules, which this extension cannot loosen) or `--read-only`. The advisor block is the one sticky piece of state here.

**It does not care whether a call was asked for.** An earlier version had an `out_of_scope` question. Live, it flagged `echo ok` at 0.89 because nobody had asked for `echo ok`; in probes it scored a second typo fix in a neighbouring file at 0.93, the same as leaking the environment, because the question is binary by construction and cannot grade *how far* off-task a call is. It was removed. Consequence, by design: piping a remote script into a shell, or wandering onto unrelated work, runs untouched unless it also leaves the project, destroys something or touches a secret — those two fixtures stay in the eval to keep the decision visible.

**A project under the temp root gets weaker irreversibility scores.** The model reads `tmp` in a path string as scratch whatever `in_temp_directory` says; a deletion that scores 0.83 from `/Users/dev/proj` scored 0.47–0.62 from `/private/tmp/…`. Paths inside the working directory are never marked temp, but the string is still there. Don't work under `/tmp`.

## Events

Every batch emits `core.SystemOneDecisionEvent` (`systemone.decision`), failures included — a decision model that silently gates tools or swaps the LLM is otherwise undebuggable. It carries the batch name, the **version that answered** (`jev-latest` moves; log what actually ran), duration, input tokens, and one `SystemOneDecision` per question with its answer, confidence, the action taken (`none`/`ask`/`deny`/`remind`/`route`) and its target. A non-empty `Error` means the batch changed nothing.

## Wiring

`harness.WithSystemOne(cfg, resolveModel)` registers it (re-exported as `tenzing.WithSystemOne`, with the config types aliased beside it). `harness.New` binds all four setters, and `resolveModel` — an alias→`common.LLM` func from the app layer, since the harness cannot resolve aliases itself — becomes the router. A `Config` with no client registers nothing, so the zero value is safe to pass unconditionally.

**Subagents get the gate, not the rest.** A child loop touches the same filesystem, so `harness.New` adds `Ext.ChildGate()` to every child's extension set — batch B's working-directory rule and two questions, against the same client, thresholds and failure breaker. It carries no `BeforeIteration` and skips the advisor bookkeeping: a child's first iteration would otherwise reset the main turn's advisor state and re-run routing. `TestSystemOneGateCoversSubagents` pins it.

**Routing switches the model mid-turn, deliberately.** The choice is made in the first iteration's `BeforeIteration`, at which point the loop FSM has left the idle states and `Harness.SetLLM` would refuse it (`errBusy`). The router therefore calls `Harness.switchLLM`, the same body without the between-turns guard. That is safe only at that one moment: the loop goroutine makes the choice and is the next thing to read the client, on the line after the hooks return. `Agent.model` is `RWMutex`-guarded (`internal/adapters/agent`) because a driver may be reading the model name over HTTP at the same time — `TestSystemOneRoutingIsRaceFreeAgainstAModelReader` pins it.

`cmd/app/systemone.go` builds all of this from the config: the client through `deps.judges`, the candidates from `models.llm:` entries carrying a `description:`, and the resolver from the model registry. Two defaults live there rather than here, because only that layer can tell "unset" from "zero": `RecentMessages` (0 means *send none*, so `New` cannot default it — use `DefaultRecentMessages`) and whether the advisor consumer has an `advisor` tool to consult at all.
