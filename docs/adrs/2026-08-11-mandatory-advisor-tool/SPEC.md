# SPEC: Transcript-Aware Advisor with Enforced Write-Gate (Opt-In)

Self-contained execution spec — assumes **no prior conversation context**. Companion `PLAN.md` in this directory is the terse version of the same change. Line numbers were accurate at commit `d66799a`; treat them as anchors and search for the named symbols if they've drifted.

## Ground rules (from repo `CLAUDE.md` / root `AGENTS.md` — read both first)

- Update every `AGENTS.md`/doc statement your change makes untrue **in the same change**.
- Do not commit or push — repo owner handles version control.
- No stray build artifacts (compile-check with `go build -o /dev/null ./...`).
- Table-driven Go tests; run `go test ./...` and `go test -race ./...`.
- Surgical changes; match existing style.

## Background

### The advisor today

- `internal/features/advisor/tool_advisor.go` (~96 lines) is the whole feature. `AdvisorTool{llm common.LLM}`: native tool named `"advisor"`, `ReadOnly() == true`, schema `{plan (string, required), context (string, optional)}`. `Execute` makes one stateless `SendSyncMessage` with a critique system prompt and `maxTokensStdResponse int64 = 32768`. The advisor sees **only** the strings the main model chooses to pass — no conversation history.
- Registered conditionally in `harness.New()` (`internal/harness/harness.go:275-277`): only when `o.advisorLLM != nil`, i.e. only when the CLI got `--advisor-model` (`cmd/app/root.go:114` → `roleLLM` helper in `cmd/app/options.go:96-119`; empty flag → nil → tool absent). **This conditionality is kept** — the flag remains the single opt-in switch.
- `advisor` is in the permissions `Ask` list (`internal/features/permissions/permissions.go:33`) — every call triggers an approval prompt; headless/print mode (`ApprovalTimeout(0)`) auto-denies.
- The CLI's client factory caches clients by provider+model+baseURL (`cmd/app/llm.go` ~:81-90).

### Relevant harness architecture

- Extension seams (`internal/core/extension.go`): `PromptContributor` (`PromptFragment() string`, joined into the system prompt at `harness.go:305-307`), `BeforeIterationHook` (receives `*TurnContext{Iteration, Reminders, Terminate, ...}`; reminders are appended to the system prompt per call by the agent adapter), `ToolCallHook` (receives `ToolCallContext{Call, Origin, Decision, Reason}`; decisions can only be escalated toward deny, never lowered — extension.go:194-205; permissions runs first). `WithToolCallGate` (`internal/harness/gate.go:12-27`) is an existing deny-capable hook to model from.
- Assembly order in `New()`: extensions at harness.go:153-213 (readOnly-or-permissions → tool gate → reminders → skills → budgets → mcp → blackboard → subagent → caller extensions → `core.NewExtensions`); **native tool registry after that**, at :215-219; advisor/extras at :275-285; system prompt at :287-307; main agent :309-335; composite `toolport.NewComposite(toolRegistry, allExts)` :340-346; context store `contextstore.New(...)` :350-358; runner :361-379.
- Native tools implement `ReadOnly() bool`. Extension-provided tools (MCP) may not expose it.
- Subagents (`subagent.NewSubAgentFactory`, harness.go:199-210) get no advisor — unchanged by this spec.
- No `tool_choice`/forced-tool-call support exists anywhere (`common.CompletionRequest`, `pkg/common/chat.go`, has no such field) — hence a client-side gate.

### Why this design (decisions final — do not relitigate)

Two earlier designs were considered and dropped, after comparing with Anthropic's server-side advisor tool (https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool):

1. **Turn-1 "pre-plan"** (harness force-calls the advisor before the loop): Anthropic's data shows a forced consult **before any orientation** (zero context) displaces the well-timed one, costing 3-4pp on some workloads. Their effective pattern: *orient first, consult before writing* — a "hard rule" that the first state-changing call be preceded by an advisor call, read-only commands exempt, one-line edits included.
2. **Always-on with main-model fallback**: self-advising has no supporting data (all Anthropic results are executor + *stronger* advisor pairs), re-sends the full transcript at main-model rates for modest value, and silently changes behavior for every `pkg/tenzing` embedder while breaking existing scripted-agent tests.

Final design, all keyed on the existing `advisorLLM != nil` switch:

- **Transcript-aware advisor**: the tool automatically sees the full conversation (Anthropic's core mechanic, replicated client-side); the model passes at most an optional question.
- **Write-gate**: Anthropic's hard rule enforced in code via `ToolCallHook` — stronger than their prompt-prose version, works headless.
- **Prompt guidance** ships as the gate extension's `PromptFragment`, so it exists only when the feature does.
- **Nudge** (+7pp Haiku, ~0 Sonnet, **negative** Opus in their testing) is a second, separate opt-in.
- **Cost**: advisor `max_tokens` 2048 (their testing: ~7x output reduction, no quality loss) plus a soft "~80 words" ask.

Unset flag → behavior byte-identical to today.

## Implementation steps

### Step 1 — Transcript-aware `AdvisorTool` (`internal/features/advisor/tool_advisor.go`)

- Constructor: `NewAdvisorTool(llm common.LLM, history func() []common.Message) *AdvisorTool`. `history` returns the main conversation's messages at call time.
- Schema: remove `plan`/`context`; add single **optional** `question` (string): "specific question for the advisor; omit for a general review of your current approach".
- `Execute`:
  1. `msgs := t.history()`; render to text: role label + text content + tool calls (name + compact args) + tool results (truncate each to a few hundred chars). Check `pkg/common/chat.go` for message/block structure and search for an existing transcript-rendering helper before writing one.
  2. Truncate **oldest-first** so transcript + prompts fit `llm.GetContextWindowSize()` minus generous headroom (system prompt + response reserve); insert `[earlier conversation truncated]` when trimming.
  3. User message: transcript block, then `question` if provided.
  4. One `SendSyncMessage{Model: llm.GetCurrentModel(), System: advisorSystemPrompt, MaxTokens: maxTokensStdResponse}`; errors → `ToolResult{IsError: true}` as today.
- New system prompt (replaces the critique prompt): senior technical advisor; you see the executor's full transcript — task, every tool call and result, its reasoning so far; identify risks, wrong assumptions, missing steps, simpler alternatives; if the approach is sound say so briefly; keep guidance under roughly 80 words unless a critical risk demands more.
- `maxTokensStdResponse`: `32768` → `2048`.
- Tool description (what the executor reads), adapted from Anthropic's: "Consult a stronger reviewer model that automatically sees your full conversation — the task, every tool call and result. No arguments needed. Call before substantive work (writing, editing, committing to an approach), when stuck, or before declaring the task done. Orientation reads first are fine."
- After a successful call: `slog.Info("advisor consulted", "model", ...)` plus token usage if `common.CompletionResponse` exposes it.

### Step 2 — Conditional wiring (`internal/harness/harness.go`)

No fallback. Inside `if o.advisorLLM != nil { ... }`:

- Register the tool with its history accessor. The context store is constructed *after* the current registration site (:275 vs :350). Options — pick whichever reads cleaner after reading the code:
  a. Move advisor registration below context-store construction (registration must still precede `NewComposite` at :340; if ordering fights you, prefer b).
  b. Late-bound closure: `var historyFn func() []common.Message` declared early; registration passes `func() []common.Message { if historyFn == nil { return nil }; return historyFn() }`; assign `historyFn` right after `contextstore.New`. Find the store's message accessor by reading `internal/harness/contextstore/` (the loop reads messages from it each iteration — reuse that accessor).
- Register the write-gate extension (Step 3) in the extension assembly block (:153-213), after the permissions/read-only extension. The extension block runs before `o.advisorLLM` is consulted today — keep the gate's registration inside the same `advisorLLM != nil` condition; restructure minimally (e.g. compute the condition once, append the ext conditionally into the slice that feeds `core.NewExtensions`).

### Step 3 — Write-gate extension (new file `internal/features/advisor/ext.go`)

A `core.Extension` implementing `ToolCallHook`, `PromptContributor`, `BeforeIterationHook`. Model plumbing on an existing small extension (the tool gate in `internal/harness/gate.go`, or whatever implements read-only mode).

- **State** (per main-agent turn): `consulted bool`, `advisorCalls int`. Reset both when `tc.Iteration == 1` in `BeforeIteration`.
- **ToolCallHook**, in order:
  1. Call is `advisor`: if `advisorCalls >= maxAdvisorCallsPerTurn` (const, `5`) → Deny, reason "advisor call cap reached for this turn; proceed with the guidance you have". Else increment, `consulted = true`, allow.
  2. Read-only tool → allow.
  3. `!consulted` → **Deny**, reason: "Call `advisor` before your first state-changing action this turn. Read-only orientation (reads, searches, listings) is allowed first. This applies to one-line edits too."
  4. Otherwise allow.
  "Allow" means *leave the decision untouched* — hook decisions only escalate.
- **Read-only lookup**: inject `func(toolName string) bool`. Native registry tools expose `ReadOnly()`; registry is built after extensions, so use a late-bound setter/closure (Step 2b trick) or reorder registry construction above extension assembly if nothing prevents it. **Unknown tools (MCP/extension-provided) count as state-changing** — fail closed.
- **PromptFragment** — the guidance the gate enforces, adapted/shortened from Anthropic's blocks:
  - Call `advisor` before substantive work — before writing, editing, or committing to an approach. Orientation first (finding files, reading what's there) is fine and encouraged; then consult.
  - Also when stuck (recurring errors, approach not converging) and once before declaring a non-trivial task done.
  - Call `advisor` **before** `TodoWrite` so its plan funnels into the todo list.
  - Give the advice serious weight; if your evidence contradicts it, surface the conflict in one more advisor call rather than silently switching.
  Living here (not `default_main.gotmpl`) makes the mandate exist only when the gate does. Check the gotmpl's `## Planning` section for wording that would conflict when the fragment is appended; adjust only if genuinely contradictory. (Known unrelated staleness: the template documents a removed `rlm` tool — surface, don't fix.)
- **Nudge** (Step 5) lives in this extension's `BeforeIteration`.
- Main loop only; subagents untouched.

### Step 4 — Permissions (`internal/features/permissions/permissions.go:33`)

Remove `advisor` from the default `Ask` list (auto-allow). Rationale: `ReadOnly() == true`, and a gate-mandated tool cannot sit behind an approval prompt that headless mode (`ApprovalTimeout(0)`) auto-denies — the run would deadlock against its own mandate. Check whether the policy model needs an explicit allow entry. Harmless when the advisor isn't registered.

### Step 5 — Opt-in nudge

- `WithAdvisorNudge(iteration int)` in `internal/harness/harness_options.go` (0 = disabled, default), re-export in `pkg/tenzing/tenzing.go`, CLI `--advisor-nudge` (int, 0) in `cmd/app/root.go` + `cmd/app/options.go`.
- Semantics: only meaningful when the advisor is enabled; if set without `--advisor-model`, log a warning and ignore (document in the flag help).
- Gate's `BeforeIteration`: if enabled and `tc.Iteration >= nudgeIteration` and `!consulted` → append to `tc.Reminders` (once per turn): "You have not consulted the advisor yet. If the task has a non-obvious design decision or a failure mode you haven't ruled out, call advisor now before committing to an approach."
- Off by default even when the advisor is on — Anthropic measured the nudge negative on strong executors.

### Step 6 — CLI/help/docs text

- `cmd/app/root.go` `--advisor-model` help: `"model for the advisor tool; setting it enables the advisor and its write-gate"`.
- `internal/harness/harness_options.go` + `pkg/tenzing/tenzing.go` comments: "enables the transcript-aware advisor tool and its write-gate when non-nil".
- `roleLLM` mapping in `cmd/app/options.go`: unchanged.

### Step 7 — Tests (table-driven)

`internal/features/advisor/` (reuse/extend `stubLLM` in `tool_advisor_test.go`):
- Transcript rendering: roles/tool calls/results present; long results truncated; oldest-first trimming under a small fake context window; `question` appended when set, omitted otherwise; empty history handled.
- Request shape: `System` is the new advisor prompt; `MaxTokens == 2048`; model from `GetCurrentModel()`.
- Gate matrix (table): read-only pre-consult → allow; state-changing pre-consult → deny (reason mentions advisor); advisor call → allow + marks consulted; state-changing post-consult → allow; Iteration 1 resets; calls beyond cap denied; unknown tool = state-changing; an already-denied decision is never lowered.
- Nudge: disabled by default; enabled + iteration ≥ N + not consulted → reminder appended once; consulted → none.

`internal/harness`:
- `TestHarnessAdvisorRegistration` (harness_test.go:154-179): semantics unchanged (nil → tool absent; set → present) — should still pass; extend to assert the gate extension is present only when set.
- Integration (opt-in): scripted agent with `WithAdvisorLLM(stub)` attempts a write tool first → denied with gate reason; calls advisor (no permission prompt) → write allowed. Advisor stub receives a request whose user message contains earlier turn content (proves history wiring).
- No-flag path: existing tests must pass **unchanged** — that's the point of opt-in. Any failure there is a bug in the conditional wiring.
- Sweep `go test ./...` and `go test -race ./...`.

### Step 8 — Doc drift (mandatory, same change; grep "advisor" in each)

- Root `/AGENTS.md`: advisor description — transcript-aware, opt-in, write-gate semantics, permissions change, `--advisor-nudge` (check ~:113 and the flag list ~:139).
- `SYSTEM_ARCHITECTURE.md`: advisor package (~:164-165), role-LLM bullet (~:464 — advisor still has **no** fallback; keep accurate), build order (~:480), tool table (~:565), flag list (~:711), extension inventory if one exists (the gate is a new extension).
- `README.md`: advisor mentions (~:97, ~:106, ~:205) — describe opt-in advisor + gate briefly.
- `internal/features/permissions` comments/docs about the default Ask list.

## Accepted risks

- **Gate strictness**: per-turn reset means every writing turn pays one advisor call, including trivial follow-ups — deliberately mirrors Anthropic's hard rule ("applies to one-line edits too"). Relax later (per-session consult, size-based exemption) if practice shows it's annoying.
- **Transcript cost**: advisor input grows with session length; oldest-first truncation and the per-turn cap bound it; provider-side prompt caching mitigates repeats.
- **Discoverability**: opt-in means most runs get no planning discipline — accepted in exchange for zero silent behavior change. Self-advising remains available explicitly (`--advisor-model` = the main model).

## Verification checklist

1. `go build -o /dev/null ./... && go test ./... && go test -race ./...`
2. **No flag**: behavior byte-identical to today — no `advisor` in tool listings, no gate denials, all pre-existing tests green without modification.
3. `--advisor-model <stronger-model>`, write task: first Edit/Write attempt denied with the gate reason; model calls `advisor` (no approval prompt); advisor response references actual transcript content; write proceeds. Advisor `SendSyncMessage` goes to the configured client (check logs).
4. Read-only turn with flag set ("what does this repo do?") → no forced consult, no denial.
5. `--advisor-nudge 3` (with advisor enabled) and a model that skips the advisor → reminder appears from iteration 3. `--advisor-nudge` without `--advisor-model` → warning, ignored.
6. All AGENTS.md / SYSTEM_ARCHITECTURE.md / README.md advisor mentions updated (grep "advisor").
7. No binaries or temp artifacts left; no commits made.
