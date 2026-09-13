# Control-Plane Fleet Support: WebSocket dial-out agent mode (protocol v1)

## Context

tenzing is run today as either a human-attended HTTP/SSE server (no `-p` flag) or a
one-shot headless turn (`-p`). There is no support for running tenzing as a *fleet*:
a master control plane spawning and supervising many tenzing processes.

The control plane **does not exist yet**; it is being built alongside tenzing by the
same team. This plan therefore covers both halves tenzing is responsible for:

1. **The agent side** — a new `--connect` mode in which a tenzing process dials the
   control plane's WebSocket server, registers itself, receives turn commands, and
   streams harness events back. This is the code tenzing ships.
2. **A versioned protocol spec** (`docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md`)
   — the contract the control-plane team implements against. The message types are
   defined in code in `internal/app/wsclient` and mirrored verbatim in the spec; the
   spec is the cross-team artifact, not a shared Go package.

Design: **agent-dials-home**. The control plane never allocates ports for agents; a
tenzing process makes an outbound-only connection, so NAT/containers/firewalls stop
mattering, and connection establishment *is* the readiness signal. Agent state lives
in-process: a dropped socket does not kill the running turn — the agent reconnects
with backoff and replays buffered events.

What is reused unchanged below the transport: the harness, `turnqueue.Queue`
(one active turn per process), the versioned `internal/app/wire` envelope contract
(event direction speaks exactly that vocabulary), session persistence, permissions,
and the event bus.

Decisions:

- **One WebSocket connection per agent process.** Typed JSON messages both ways
  (see PROTOCOL.md). Commands carry a correlation `id`; events are attributed by
  turn and agent labels already present on `wire.Envelope`.
- **New package `internal/app/wsclient`.** Owns the dialer, backoff/reconnect, and
  message dispatch. Message types live here; the spec doc mirrors them. The control
  plane is external, so there is deliberately no shared Go package — the protocol
  document is the shared contract.
- **New mode `--connect <url>` in `cmd/app`.** `runConnect` (alongside
  `runServe`/`runPrint`) wires bus + logging + trust/project-config + `harnessOptions`
  and the harness — but **no `api.Server`, no HTTP listener, no nexus** (nexus's
  trigger target is `srv.StartNexusTurn`; re-adding nexus waits for a turn-injection
  seam). `NewAppContainer` stays untouched; connect mode assembles its own reduced
  wiring.
- **`hello` registration with capabilities, protocol version, and last-turn state.**
  The agent's first message after connect:
  `{type:"hello", protocol:"1", pid, cwd, model, capabilities, last_turn?}`
  (capabilities: vision, approvals-supported; `last_turn` carries
  `{id, outcome: cancelled_disconnect|completed|error}` when a turn was in flight at
  the previous disconnect, so the control plane resyncs truthfully instead of
  inferring). The control plane replies `{type:"welcome", agent_id}` — registration is
  part of the handshake, which is why the stdout readiness line and
  `--advertise-file` (listen-model era) are dropped for this contract.
- **A dropped connection cancels the in-flight turn.** This is the load-bearing
  lifecycle rule: the turn is only meaningful while the control plane can observe it —
  an orphaned turn burning tokens with nobody watching is worse than a cancelled one,
  and the control plane owns the retry decision. On socket loss the wsclient cancels
  the running turn's context (the same path `turnqueue.Queue.Cancel` uses,
  `api/turnqueue/turnqueue.go:119`): the loop classifies it as a clean outcome
  (`internal/core/loop.go:575` — `turn canceled`, no ErrorEvent, FSM reusable), a
  `result` message with `outcome:"cancelled_disconnect"` is *queued* (not lost), and
  the turn's buffered event backlog is discarded, not replayed. Reconnect then re-hellos
  (with `last_turn` reporting the cancellation) and starts clean — there is no
  mid-stream replay to do. Cancellation is graceful: running tool calls resolve per
  harness semantics, and the session transcript (including partial work) is already
  persisted by session persistence, so a retry via resume isn't from scratch. Wasted
  tool work on a mid-flight cancel is accepted: correctness (no unseen agent) over
  thrift. Two edge cases, stated explicitly: a pending approval is broken by the same
  turn context (verified: `waitApproval` selects on `ctx.Done()`,
  `internal/core/approval.go:71`), so the turn cannot stall forever on an unanswered
  approval after a disconnect; and if the connection dies mid-`result`, the queued
  result may never reach the control plane — acceptable, since the control plane sees
  the disconnect and the turn is already dead locally; the `cancelled_disconnect`
  result is advisory — the disconnect itself is the authoritative signal.
- **Reconnect with jittered exponential backoff; the process stays alive.** On
  reconnect: re-send `hello` (including `last_turn` when applicable) and resume
  immediately — the agent self-reports idle and does not wait for permission to accept
  commands; the control plane simply sees `hello.last_turn: cancelled_disconnect`
  before the next `query`. A protocol-version mismatch at handshake is **transient**:
  the agent logs the refusal and retries with backoff (a deploy gap may resolve);
  only startup/config errors exit non-zero (the control plane must distinguish "bad
  config, don't retry" from "control plane unreachable, keep dialing").
- **Approvals are first-class push.** The permission extension's
  `ApprovalRequestedEvent` is forwarded as `{type:"approval_request", call_id, tool,
  input}`; the control plane answers `{type:"approve", call_id, approved, glob?}`.
  Fleet config should either use a long `approval_timeout` or
  `--dangerously-skip-permissions` for sandboxed fleets.
- **Auth**: bearer token on the upgrade request (`Authorization` header) or in
  `hello`; `wss://` termination is the control plane's job. Agents initiate, so no
  inbound firewall rules — the main reason the pattern fits fleets.
- **Dependency**: `github.com/coder/websocket`. Hand-rolled framing is not on the
  table.
- **Stdout readiness line kept (not the advertise file).** The one useful survivor of
  the listen-model plan: a single `{"type":"ready","pid":N,"addr":"..."}` JSON line
  before dialing gives the control plane crash-before-dial visibility; logs already go
  to the log file, so stdout stays clean protocol lines only. `--advertise-file` is
  obsolete under dial-out and is not built.

## Steps

1. **`internal/app/wsclient` — protocol types + client.**
   Message structs (hello/welcome/event/approval_request/approve/result/steer/cancel/
   query/set-model/set-thinking), dial with token, read pump (JSON-decode dispatch),
   write pump (single goroutine, mutex on send), jittered exponential backoff with cap,
   and disconnect detection wired to the turn-cancel hook: on connection loss the
   client cancels the in-flight turn context and discards that turn's event backlog.

2. **`cmd/app/connect.go` — `runConnect`.**
   Reduced wiring: `setupLogging`, project-config + trust resolution
   (`loadProjectConfig`), `harnessOptions`, `harness.New`, `eventbus` subscription →
   wsclient. Model resolution and config precedence identical to serve mode
   (`buildDeps`, `cfg.deps`). Turn execution goes through `harness.RunTurnWithImages`
   (same call print mode makes), one turn at a time, mirroring turnqueue semantics,
   with each turn's context derived from the connection liveness — socket down =
   turn context cancelled.

3. **Flag surface** (`cmd/app/options.go`, `cmd/app/root.go`).
   `--connect <ws-url>` (mutually exclusive with `-p`; serve-mode flags ignored with
   the existing warning pattern), `--connect-token <env|value>`, `--connect-backoff`
   (base delay, optional). tenzing.yaml gets a `connect:` section via
   `internal/config` in the same change (strict schema: unknown keys error) — fleets
   are spawned from config by definition, so flag-only would get wrapped in shell
   scripts immediately. Precedence as everywhere else: flag > env (`TENZING_CONNECT`,
   `TENZING_CONNECT_TOKEN`) > tenzing.yaml `connect:` > default.

4. **PROTOCOL.md** — the versioned cross-team spec and the **sole normative contract**
   for the wire protocol; this plan defers to it and does not restate message shapes.
   Contents: message tables both directions, correlation rules, hello/welcome
   handshake (including `last_turn` resync reporting), disconnect semantics (turn
   cancelled, backlog discarded, `cancelled_disconnect` outcome on next hello), auth,
   error taxonomy (config error vs. transient), backpressure semantics (a slow control
   plane blocks the agent's write pump — `eventQueue` buffers the bus side losslessly;
   the turn keeps running but events are not acknowledged until drained; no drop), and
   versioning rule (additive = minor, breaking = bump `protocol`).

5. **Testing** (`cmd/app`, `internal/app/wsclient`):
   - In-repo WS test double: `httptest` server accepting the upgrade
     (`coder/websocket.Accept`), speaking the protocol; lives in test files only.
   - Handshake: hello → welcome with `agent_id` assignment; wrong-protocol-version
     refusal.
   - Event streaming: stub harness emits bus events; double asserts envelopes arrive
     in order, `result` last.
   - Approvals: approval_request → approve round-trip drives the harness decision.
   - Disconnect cancels the turn: kill the double mid-turn; assert the harness turn
     aborts with `turn canceled` (no ErrorEvent), the `result` outcome is
     `cancelled_disconnect`, the dead turn's buffered events are discarded, and the
     FSM is reusable (race detector on).
   - Reconnect: agent reconnects, re-hellos with `last_turn` reporting the
     cancellation, and a fresh query runs cleanly on the reused loop.
   - Config errors exit non-zero without dialing.

6. **Doc updates.** `AGENTS.md` CLI section (new mode + flags + protocol pointer),
   `SYSTEM_ARCHITECTURE.md` (new mode in the CLI section, `internal/app/wsclient` in
   the layout table).

## Non-goals

- No control-plane implementation in this repo (external, built against PROTOCOL.md).
- No mid-stream event replay on reconnect — disconnects cancel turns by design, so
  there is no backlog to replay; `hello`'s `last_turn` reports the outcome instead.
- No turn multiplexing per connection — one active turn per process, mirroring
  turnqueue.
- No nexus in connect mode (trigger target is serve-mode's `srv`); future work.
- No HTTP listener, no embedded UI in connect mode; serve mode remains the
  human-attended path.
- No TLS/auth beyond the bearer token + wss at the control plane.
- `--advertise-file` from the listen-model draft: not built (obsolete under dial-out).

## Alternative kept on record: HTTP-serve supervision

The listen-model contract (serve mode + readiness line + `--advertise-file`) remains
viable for HTTP-only supervisors and is documented above in git history; only the
stdout readiness line carries forward as an optional companion to this plan
(crash-before-dial visibility). It is not built as part of this plan.