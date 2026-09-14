# Tenzing Harness Changes for the Control Plane

Status: **implemented 2026-09-13** (see `PLAN.md` alongside for the design and the
review corrections it records). Enumerates every change needed in `tenzing-agent-harness`
to realize `docs/CONTROL_PLANE_VISION.md`, based on a source-level review
of the harness (protocol: `docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md`,
TCP-WS v1; client: `internal/app/wsclient`). Related control-plane docs:
`docs/HARNESS_PROTOCOL.md` (protocol adoption/mapping, §5 summarizes the
gap list kept canonical here) and `docs/CONTROL_PLANE_VISION.md` (the
parent spec). The good news up front: the harness already speaks the
control-plane protocol, resumes sessions, and takes its
toolset/permissions from config — the required-change list is small.

**Ground rule**: any change to the TCP-WS message set must be *additive*
(see PROTOCOL.md §6 — new optional fields and new message types do not
bump `protocol`); anything breaking bumps the version and requires
coordinated rollout with the control plane.

## 1. Required changes

### 1.1 Report the conversation id in `hello` — small, additive — DONE

Shipped as `hello.conversation_id` (omitted when session persistence is off);
`internal/app/wsclient/messages.go:Hello`, wired from `Harness.ConversationID()`
in `cmd/app/connect.go`.

**What**: add an optional `conversation_id` field to the `hello` message
(`internal/app/wsclient/messages.go:Hello`), populated from the harness's
active conversation (empty when sessions are disabled or the id is not yet
assigned).

**Why**: the control plane must persist an attempt → conversation-id
mapping per node (vision doc §4 persistence decision) so it can
`--resume` after a restart and support rewind/restart-from-earlier-node
(vision doc §2.1 human resume options). Today a fresh session's id is
invisible over the protocol: `hello` carries pid/cwd/model/capabilities
only, and no session-list endpoint exists on the socket.

**Proposed shape**: `hello {…, conversation_id?: string}` — additive, no
version bump.

**Fallback without this change**: the plane pre-assigns the id at launch
via `--conversation-id` (`cmd/app/root.go:186` — verified present), so it
always knows the id. This workaround is adequate for v1; the `hello` field
is still preferred because it also confirms what id the harness actually
settled on (e.g., after a resume of a conversation that rotates ids).

### 1.2 Ephemeral grant mode for approved globs — small — DONE

**Correction found during implementation**: the premise below was wrong for
connect mode. `cmd/app/connect.go` wired `Approve` with the glob discarded
(`_ string`), so `approve.glob` neither persisted nor took effect — "allow
always" was a silent no-op over the socket. The change therefore *added*
grant handling rather than suppressing a durable write.

**Shipped**: `connect.ephemeral_grants` (tenzing.yaml) /
`--connect-ephemeral-grants` / `TENZING_CONNECT_EPHEMERAL_GRANTS`, default
`true`: grants go to the live `BashRules` in memory only. `false` persists via
`BashAllowStore.Add` like serve mode. See `connectApprove` in
`cmd/app/connect.go`.

**Original text**: the `approve {glob}` allow rule currently **persists durably**
— approved bash globs are appended to `settings.json` via
`BashAllowStore` (`cmd/app/options.go:65-67`, `cmd/app/settingsfile.go`).
Add a connect-mode option (config key or flag, e.g. `connect.ephemeral_grants:
true`) that keeps runtime-approved glob rules in memory only.

**Why**: the control plane's gate model decides per-node grants are
**ephemeral — they die with the node and never persist across reruns**
(`HARNESS_PROTOCOL.md` §4). A durable settings.json would leak one node's
approvals into later attempts and other runs.

**Fallback without this change**: the plane points `--settings` at a fresh
per-attempt file (it already generates per-node config), so durable grants
land in a throwaway file. Adequate, but wasteful and easy to get wrong.

### 1.3 `shutdown` command — small, optional but recommended — DONE

**Correction found during implementation**: `cmd/app/main.go` trapped
`os.Interrupt` only, so SIGTERM was a hard kill — the deferred
`Harness.Shutdown()` never ran and session persistence was not flushed. Fixed:
SIGTERM now takes the same graceful path as SIGINT (all modes).

**Shipped**: plane → agent `shutdown {id}`; agent replies
`result{outcome:"cancelled"}` for the running turn (if any), then
`shutdown_ack {id}`, closes normally, does not reconnect, and exits 0 after
`Harness.Shutdown()`. Older agents drop the unknown type (PROTOCOL.md §6).

**What**: add a plane → agent `shutdown` command that finishes/aborts the
in-flight turn gracefully, flushes session persistence, acks, and exits 0.

**Why**: the plane's kill semantics (vision doc §2.3) currently have no
graceful path — the convention is `cancel` (turn-level) then SIGTERM then
SIGKILL (`HARNESS_PROTOCOL.md` §5). A protocol-level shutdown makes node
teardown deterministic and avoids SIGTERM-during-a-turn ambiguity. Note
the harness already has a clean internal path (`Harness.Shutdown()`,
`internal/harness/harness.go:518`) — this change mostly just exposes it
over the socket.

## 2. Nice-to-have (defer unless cheap)

### 2.1 Files-touched summary on `result` — DONE

**Correction found during implementation**: `FileTracker` is a read-before-edit
freshness map (Read, Edit *and* Write record into it, session-wide, not per
turn) and is not reachable from `cmd/app`. The summary is instead tallied from
`core.ToolSucceededEvent` in connect mode's event pump: `file_path` of every
**successful** Read/Edit/Write call during the turn (main agent and
subagents), deduplicated, first-seen order. Denied/failed calls are excluded.

**Shipped**: `result.files_touched?: [paths]` (omitted when empty). Side
effect: `result.denied_tools` is now reset per turn as PROTOCOL.md §2
specifies (it previously only reset on disconnect, accumulating across turns).

**What**: add an optional `files_touched: [paths]` to the `result` message
(collected from the FileTracker stamps the tool registry already keeps).

**Why**: the judge's evidence is "the agent's final message plus whatever
it could access in its sandbox" (vision doc §2.1). The plane can read the
sandbox directly, so this is not required — but a harness-reported access
summary would make the judge's prompt more faithful without the plane
re-deriving access.

### 2.2 Deny-detail in the event stream

**What**: confirmed already exists — `ToolDeniedEvent` (tool, input,
reason) is serialized into the wire stream
(`internal/app/wire/wire.go:276`). **No harness change**; the plane parses
`event.envelope` types for the audit log. Moved here from §2/§3 to keep
the required list honest: this is plane-side parsing only.

## 3. Verification items (no change expected)

- **`hello.conversation_id` vs `--conversation-id`**: confirm a
  pre-assigned id survives id rotation on resume (the plane relies on the
  pre-assign workaround until §1.1 lands).
- **Connect-mode + `--resume` together**: **verified** by
  `TestConnectResumeEndToEnd` (`cmd/app/connect_e2e_test.go`): two
  `runConnect` runs against an in-process plane double with a scripted LLM
  (`deps.llms` is now the `llmSource` interface so tests can inject one) —
  run 1 pre-assigns the id and exits on `shutdown`; run 2 `--resume`s, reports
  the same `hello.conversation_id`, and the model receives run 1's history.
  Also found and fixed on the way: `runConnect` passed a nil tee writer to
  `setupLogging`, so connect mode panicked on its first log line.
- **`ready` stdout line**: verified — connect mode prints
  `{"type":"ready","pid":…,"mode":"connect","connect_url":…}` on stdout
  after harness init, before dialing (`cmd/app/connect.go:80-88`); it is
  the only protocol stdout line, giving the plane crash-before-dial
  visibility. The plane learns *dial* readiness from receiving `hello`.
- **Event envelope taxonomy**: inventory `internal/app/wire` event types
  the plane must parse for the live feed and audit (tool calls, results,
  denials, compression). Plane-side work; listed here so the audit design
  knows what exists.

## 4. Explicitly NOT needed (checked against the vision)

- **State reporting**: node state is derived by the plane from protocol
  traffic (turn in flight / approval pending / result). No `state.report`
  message; no change.
- **Revise loop**: a follow-up `query` on the live process; no
  `revise.prompt`; no change.
- **Exit-condition judging**: entirely control-plane side (judge LLM +
  sandbox reads). No harness involvement.
- **Common toolset delivery**: the plane renders a per-node `tenzing.yaml`
  (`permissions:`, `mcp_servers:`, `providers:`/`models:`) — existing
  config machinery, no change.
- **Sandbox confinement**: deferred non-goal (vision doc §3); the harness
  runs with cwd = project root. No change.
- **Concurrency**: one turn at a time per agent process is exactly the
  per-node model the plane wants. No change.
- **Auth**: bearer token on the upgrade request already exists
  (`connect.token`). No change.
- **Output streaming**: `event` envelopes already stream losslessly with
  backpressure; the plane persists them. No change.

## 5. Suggested order

All items above shipped together on 2026-09-13; the order actually used and
the per-step verification are in `PLAN.md` §7.