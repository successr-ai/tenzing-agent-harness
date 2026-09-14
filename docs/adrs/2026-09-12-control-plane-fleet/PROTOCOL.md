# Tenzing Control-Plane Protocol (TCP-WS v1)

**This document is the sole normative contract** between a tenzing agent process
(`--connect` mode) and its control plane. The Go implementation
(`internal/app/wsclient`) mirrors this spec; where the two could drift, this
document wins and the code is corrected.

Conventions: one WebSocket connection per agent process. All messages are JSON
objects on text frames. Messages are small (one frame, no fragmentation reliance);
event payloads may be large but never stream across frames. The connection carries
commands (control plane → agent) and messages (agent → control plane) multiplexed;
commands carry a correlation `id` echoed by the agent's responses to that command.

## 1. Handshake

The agent dials `<connect.url>` with header `Authorization: Bearer <token>` (when a
token is configured) and subprotocol `tenzing.v1`. The server MUST accept the
upgrade and reply `welcome`:

| # | Direction | Message |
|---|-----------|---------|
| 1 | agent → plane | `{type:"hello", protocol:"1", pid, cwd, model, capabilities, last_turn?, conversation_id?}` |
| 2 | plane → agent | `{type:"welcome", agent_id}` |

- `protocol` — `"1"`. Mismatch: the plane replies `{type:"error", code:"protocol_version", detail}` and closes; the agent treats it as **transient** (retry with backoff; a server upgrade may follow).
- `pid`, `cwd` — integers/strings, informational, for the plane's process table.
- `model` — the resolved main model's wire name at startup.
- `capabilities` — object `{vision: bool, approvals: bool}`. `approvals` is true when
  the agent is running with an approval path (i.e. not `--dangerously-skip-permissions`
  / `--read-only`); the plane MUST NOT expect `approval_request` otherwise.
- `last_turn` — present only when a turn was in flight at the previous disconnect:
  `{id, outcome:"cancelled_disconnect"}`. On a first connect it is absent. The agent
  sends `hello` and **resumes immediately**: it is idle and accepts commands without
  waiting for permission.
- `conversation_id` — the harness's active conversation id, the value the plane passes
  to `--resume` to relaunch this agent against the same history. Present when session
  persistence is on; absent when it is disabled. Authoritative over any id the plane
  pre-assigned with `--conversation-id` (it is what the harness actually settled on).

After `welcome`, the agent is registered; the plane MAY send commands at any time.

## 2. Agent → plane messages

| Type | Fields | When |
|------|--------|------|
| `event` | `{turn_id, envelope}` | Every harness event of the running turn. `envelope` is the versioned `internal/app/wire` envelope verbatim (`v`, `type`, `ts`, `runner_id`, `data`). |
| `approval_request` | `{turn_id, id, tool, input}` | A mutating tool call needs approval. The turn blocks until `approve` or cancellation/timeout. |
| `result` | `{turn_id, id, outcome, answer?, error?, denied_tools, files_touched?}` | End of a turn. `outcome`: `"completed" \| "error" \| "cancelled" \| "cancelled_disconnect"`. `id` is the correlation id of the `query` that started the turn. Exactly one `result` per accepted `query`. |
| `shutdown_ack` | `{id}` | Reply to `shutdown`, after the running turn's `result` (if any). The agent closes the connection normally and exits 0 right after. |

`result.denied_tools` counts tool calls denied by the permission policy during the
turn (mirrors print mode's stderr summary). `result.files_touched` lists the paths of
**successful** Read/Edit/Write tool calls during the turn (main agent and subagents),
deduplicated in first-seen order; omitted when empty. Denied and failed calls touched
nothing and are excluded. It is an access summary for the plane's judge, not a diff —
the plane reads the sandbox for content.

## 3. Plane → agent messages (commands)

| Type | Fields | Effect |
|------|--------|--------|
| `query` | `{id, query, images?}` | Start a turn (queued if one is running; FIFO). Reply: `result` when the turn ends, with this `id`. `images` is the queryInput image array (media_type + base64 data). |
| `steer` | `{id, message}` | Inject mid-turn steering. Acknowledged by an `event` (SteeringInjected) — no dedicated reply. |
| `cancel` | `{id}` | Cancel the running turn (and drop queued ones). The turn ends with `result{outcome:"cancelled"}`. |
| `approve` | `{id, call_id, approved, glob?, scope?}` | Answer a pending `approval_request`. `glob` adds a bash allow rule when `approved`. `scope` is `"always"` (default) or `"session"`. `session` keeps the glob **in memory for the process lifetime**. `always` persists it to settings.json in serve-mode parity — except that connect mode's default `connect.ephemeral_grants: true` still keeps it in memory; set it to `false` to persist. |
| `set-model` | `{id, model}` | Switch the main model (same validation as POST /model). Errors: `{type:"error", id, detail}`. |
| `set-thinking` | `{id, enabled}` | Toggle reasoning. |
| `shutdown` | `{id}` | Graceful teardown: cancel the running turn (it reports `result{outcome:"cancelled"}`) and drop queued ones, then reply `shutdown_ack{id}`, close normally, and exit 0. The agent does not reconnect. |

Correlation: every command's `id` is echoed on the message that answers it. Commands
that produce no dedicated reply (`steer`) are acknowledged by their side effects.
The control plane MUST generate unique correlation ids; reusing an id across
concurrent commands produces two results with that id, so disambiguation is the
plane's responsibility.

## 4. Lifecycle

- **Turn scope**: one active turn per agent process (mirrors `turnqueue`).
  Additional `query` commands queue FIFO; each gets its own `result` in order.
- **Disconnect cancels the turn.** The agent detects connection loss (read error,
  write error, close frame) and cancels the in-flight turn's context. Pending
  approvals break with the same context. The turn's buffered-but-unsent event backlog
  is discarded; the turn's `result` is queued with
  `outcome:"cancelled_disconnect"` and reported on the *next* connection in
  `hello.last_turn` (the result itself may never arrive if the connection is dead —
  the plane infers the outcome from the disconnect).
- **Reconnect**: jittered exponential backoff (base 1s, factor 2, cap 30s, full
  jitter), forever, until the process is killed. On success the agent re-hellos
  (including `last_turn` when applicable) and is immediately idle.
- **Startup errors are fatal**: config load failure, unknown model, unreachable
  protocol handshake after the first successful dial — the process exits non-zero so
  the plane can distinguish "bad config, don't restart" from "plane unreachable,
  agent still dialing." Transient dial failures never exit.
- **Shutdown**: `shutdown` is the deterministic teardown — cancelled `result` (if a turn
  was running), then `shutdown_ack`, then a normal close; the process flushes session
  persistence and exits 0. SIGTERM is the equivalent without an ack (same cancel, same
  flush, same exit code). Neither path reconnects. An agent older than this command
  drops it as unknown (§6); the plane then falls back to SIGTERM.
- **Backpressure**: a slow plane blocks the agent's write pump. Events buffer
  losslessly in an unbounded per-turn queue on the bus side; nothing drops. A
  connected-but-slow plane does NOT cancel the turn — only a *disconnected* one does.

## 5. Auth

Bearer token on the upgrade request; the plane MUST verify it before accepting.
`wss://` termination is the plane's responsibility. Agents initiate; no inbound
firewall rules.

## 7. Agent-side wiring contract (implementation notes)

The Go client (`internal/app/wsclient`) exposes two pieces its embedding wiring
(`cmd/app/connect.go`) relies on — relevant when porting the agent side to another
language:

- `SendEvent(ctx, turnID, envelope)` **blocks** on the connection's context when the
  outbound buffer is full: backpressure propagates to the event pump (and the event
  bus behind it), and events are never dropped while the connection lives. The send
  releases only when the connection's context fires (disconnect) — the backlog is
  discarded by the lifecycle rule, not by the sender.
- `ConnContext()` exposes that connection context for exactly this purpose; the
  wiring's event pump passes it rather than the process context, so a dead
  connection unblocks the pump.

## 6. Versioning

`protocol` is the major version. Additive changes (new optional fields, new message
types the old plane drops or errors on) do not bump it. Breaking changes (field
removal/renaming, semantic changes to existing types) bump `protocol` and the agent
refuses to speak an unrecognized version: it logs the refusal and retries with
backoff (the plane may be older than the agent; a deploy gap is transient).