# Advisor Alignment: Transcript-Aware Advisor + Enforced Write-Gate (Opt-In)

## Context

Two earlier revisions of this plan died honestly:

1. A deterministic harness "pre-plan" call at turn 1 — dropped after comparing with Anthropic's server-side advisor tool (https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool): their measured data shows a forced **zero-context** consult (before the executor has read anything) displaces the well-timed one and can cost 3-4pp task performance. Their effective pattern: **orient first, then consult, and never write before consulting**.
2. An always-on variant where the advisor fell back to the main model — dropped because self-advising has no supporting data (all of Anthropic's results are executor + *stronger* advisor pairs), it re-sends the full transcript at main-model rates for modest value, and it silently changes behavior for every `pkg/tenzing` embedder and breaks every scripted-agent test by default.

Final direction — **opt-in**, aligned with Anthropic's design as far as a client-side, provider-neutral tool can be. `--advisor-model` / `WithAdvisorLLM` is the single switch. Unset → exactly today's behavior: no advisor tool, no gate, nothing changes. Set → all of the following activate:

1. **Transcript-aware advisor** (their core mechanic): advisor sees the real conversation automatically; the model no longer summarizes its own work into a `plan` argument.
2. **Enforced write-gate** (their "hard rule", but in code): first state-changing tool call in a turn must be preceded by an advisor consult; read-only orientation flows freely.
3. **Prompt blocks**: their timing guidance (consult before substantive work / when stuck / before declaring done; advisor before TodoWrite), injected via the gate extension's `PromptFragment` — so they exist only when the feature does.
4. **Nudge as opt-in backstop** for weak executors (`--advisor-nudge N`; +7pp Haiku, negative on Opus — so off by default even when advisor is on).
5. **Cost hygiene**: advisor MaxTokens 32768 → 2048, ~80-word soft brevity ask, per-turn call cap, calls logged instead of invisible.

Dropped entirely: `GeneratePlan` / pre-plan / query injection / main-model fallback.

## Step 1 — Transcript-aware `AdvisorTool` (`internal/features/advisor/tool_advisor.go`)

- Constructor becomes `NewAdvisorTool(llm common.LLM, history func() []common.Message)`.
- Schema: drop required `plan`; single optional `question` string ("specific question for the advisor; omit for a general review").
- `Execute`: render the transcript from `history()` (roles, text, tool calls + truncated tool results), truncate oldest-first to fit `llm.GetContextWindowSize()` minus headroom, append the optional question, one `SendSyncMessage`.
- System prompt rewritten: senior advisor who sees the executor's full transcript; risks, wrong assumptions, missing steps, simpler alternatives; under ~80 words unless a critical risk needs more.
- `maxTokensStdResponse` 32768 → `2048`.
- Tool description rewritten along Anthropic's: no parameters needed, full history forwarded automatically, call before substantive work / when stuck / before declaring done.
- Log each consult (`slog.Info`: model, token usage if the response exposes it).

## Step 2 — Conditional wiring (`internal/harness/harness.go`)

Registration stays conditional on `o.advisorLLM != nil` (no fallback). When set:
- Register the tool with a history accessor `func() []common.Message` backed by the main context store. The store is constructed after the current registration site (:275 vs :350) — either reorder or use a late-bound closure (see SPEC).
- Register the write-gate extension (Step 3) in the extension assembly.

## Step 3 — Write-gate extension (new `internal/features/advisor/ext.go`)

Implements `core.ToolCallHook` + `core.PromptContributor` + `core.BeforeIterationHook`. Only registered when advisor is enabled.

- Per-turn state, reset at `tc.Iteration == 1`: `consulted bool`, `calls int`.
- On tool call: `advisor` → count, mark consulted (deny above per-turn cap, e.g. 5). Read-only tool → allow. State-changing tool while `!consulted` → **Deny**: "call `advisor` before your first state-changing action this turn; read-only orientation is allowed" (applies to one-line edits too — Anthropic's hard rule).
- Read-only-ness from the native registry's `ReadOnly()`; unknown (MCP/extension) tools count as state-changing.
- `PromptFragment()`: the adapted timing + treat-advice blocks (Anthropic's wording, shortened), including "call advisor before TodoWrite". Lives here, not in `default_main.gotmpl`, so the mandate exists only when the gate does.
- Main loop only; subagents unchanged.

## Step 4 — Permissions (`internal/features/permissions/permissions.go:33`)

Move `advisor` out of the `Ask` list (auto-allow). It is `ReadOnly() == true`, and a gate-mandated tool cannot sit behind an approval prompt that headless mode auto-denies.

## Step 5 — Opt-in nudge

- `WithAdvisorNudge(iteration int)` (0 = off, default) + CLI `--advisor-nudge N`. Requires advisor enabled; error or ignore otherwise (pick one, document it).
- In the gate's `BeforeIteration`: if enabled, `tc.Iteration >= N`, and not consulted this turn → append Anthropic's nudge text to `tc.Reminders`, once per turn.

## Step 6 — Docs/help/tests

- Flag help (`cmd/app/root.go:114`): "model for the advisor tool; setting it enables the advisor and its write-gate". Option comments in `harness_options.go`, `pkg/tenzing/tenzing.go`.
- AGENTS.md / SYSTEM_ARCHITECTURE.md / README.md: transcript-aware behavior, write-gate semantics, permissions change, nudge flag.
- Tests (table-driven): transcript rendering + truncation + request shape; gate matrix (read-only pre-consult allowed, write pre-consult denied, write post-consult allowed, per-turn reset, cap, unknown tool = write); nudge on/off; registration conditional (unchanged semantics: nil → absent); permissions allow. Existing tests unaffected unless they opt in — churn confined to new tests.

## Risks

- **Gate strictness**: per-turn reset means every writing turn pays one advisor call, including "fix the typo" follow-ups — mirrors Anthropic's rule ("applies to one-line edits too"). Relax later (per-session, or size-based exemption) if annoying.
- **Transcript size**: advisor input grows with the session; oldest-first truncation + per-turn cap bound it; provider-side prompt caching mitigates repeat cost.
- **Discoverability**: opt-in means most runs get no planning discipline; that's the accepted trade for zero silent behavior change. Self-advising is still available explicitly (`--advisor-model` = main model).

## Verification

1. `go build -o /dev/null ./... && go test ./... && go test -race ./...`
2. No flag: behavior byte-identical to today (no advisor tool in listings, no gate denials).
3. `--advisor-model <stronger>`: write task → first Edit/Write denied until advisor called (no approval prompt); advisor answer references actual transcript content; write then proceeds.
4. Read-only turn with flag set → no forced consult.
5. `--advisor-nudge 3` with a model that skips the advisor → reminder from iteration 3.
