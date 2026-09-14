# Plan: Control-Plane Changes to the Harness

Status: **implemented 2026-09-13**. Implements every item in
`TENZING_CHANGES.md` (same directory), including the §2 nice-to-haves and the
connect+resume e2e test. Source review on branch `tbright/ws-dial-refactor`.

Found and fixed along the way (outside the original list):

- `runConnect` passed a nil tee to `setupLogging`, so `io.MultiWriter(logFile,
  nil)` panicked on the first log line — connect mode crashed at startup.
  `setupLogging` now skips the tee when nil.
- `TestConnectSerialQueue/flush…` launched q1 and q2 without waiting for q1 to
  hold the slot; when q2 won, the test deadlocked (surfaced as a 10-minute
  `cmd/app` timeout under `-race`). It now waits for q1 first.

## 0. Corrections to TENZING_CHANGES.md (found during review)

Three premises in the change list do not match the code. Fix the doc as part
of the work; the plan below is written against reality.

| § | Doc says | Code does | Consequence |
|---|----------|-----------|-------------|
| 1.2 | connect-mode `approve.glob` persists durably to settings.json | `cmd/app/connect.go` wires `Approve: func(callID, approved, _ string)` — the glob is **dropped**; nothing is written, nothing is allowed | "Allow always" is a silent no-op in connect mode today. The change *adds* grant handling (ephemeral by default, durable when configured). |
| 1.3 | plane kill path is `cancel` → SIGTERM → SIGKILL, harness has a clean internal `Shutdown()` | `cmd/app/main.go` traps **`os.Interrupt` only**. SIGTERM is the Go default: immediate exit, deferred `h.Shutdown()` never runs | SIGTERM is not a graceful path today. One-line fix, independent of the `shutdown` command. |
| 2.1 | `files_touched` collectable from `FileTracker` stamps | `FileTracker.Record` is called by Read, Edit **and** Write — a read-before-edit freshness map, session-wide, not per turn, and not reachable from `cmd/app` | Source it from `core.ToolSucceededEvent` (`ToolName`, `Input`) on the bus instead — already observed in `connect.go`'s event pump, per turn, includes subagents. |

## 1. `hello.conversation_id` (§1.1)

Additive field, no version bump.

| File | Change |
|------|--------|
| `internal/app/wsclient/messages.go` | `Hello.ConversationID string \`json:"conversation_id,omitempty"\`` |
| `internal/app/wsclient/client.go` | `Handlers.ConversationID func() string` (optional — nil ⇒ omitted); `hello()` fills it |
| `cmd/app/connect.go` | wire `ConversationID: func() string { if dir, _ := h.SessionInfo(); dir == "" { return "" }; return h.ConversationID() }` — empty when persistence is disabled |
| `PROTOCOL.md` §1 | `conversation_id?` on the hello row + bullet: "present when session persistence is on; the id the harness settled on (authoritative over `--conversation-id`)" |

Tests (`internal/app/wsclient/client_test.go`, `double_test.go`): `handlersFor`
gains `ConversationID`; extend `TestHandshakeRegistersAgent` to assert the
field; reconnect case asserts the same value on the second hello.

## 2. LLM seam in `cmd/app` + connect+resume e2e (§3 item 2)

### 2a. Seam

`cmd/app/deps.go`: `deps.llms` becomes an interface, satisfied by the
existing factory unchanged:

```go
type llmSource interface {
    Get(rm modelregistry.ResolvedModel) (common.LLM, error)
}
```

`buildDeps` still assigns `modelregistry.NewFactory()`. Zero production
behaviour change; tests inject `&fakeLLMs{llm: ...}`.

### 2b. Test fixtures (`cmd/app/connect_e2e_test.go`)

- `scriptedLLM`: `common.LLM` fake modelled on
  `internal/harness/integration_test.go`'s `resumeFakeLLM`; records every
  `CompletionRequest` it receives; answers a fixed final text.
- `planeDouble`: `httptest` server + `coder/websocket` accept; reads `hello`,
  sends `welcome`, exposes `send(v)` / `read(&m)` (port the shape of
  `internal/app/wsclient/double_test.go` — it is test-package-private there).
- Drive `runConnect(ctx, cfg, harness.WithSessionDir(tmp))` directly; the
  variadic `extraOpts` seam already exists.

### 2c. `TestConnectResumeEndToEnd`

1. Run 1: `cfg.ConversationID = "conv-e2e"`, `cfg.ConnectURL = double.url()`.
   Plane reads hello (assert `conversation_id == "conv-e2e"`), sends `query`,
   reads `result{completed}`, sends `shutdown` (§4b), reads `shutdown_ack`;
   `runConnect` returns nil.
2. Run 2: `cfg.Resume = "conv-e2e"`, fresh `scriptedLLM`. Plane reads hello
   (same id), sends `query`, reads `result`. Assert the LLM's captured request
   history contains run 1's user query and answer — proof the session file
   was flushed by shutdown and restored through `--connect --resume`.
3. Assert the second dial happened on a fresh process context — no reconnect
   loop interference (the test's plane double counts accepted connections).

Also add to `cmd/app/options_test.go`: `cliConfig{ConnectURL, Resume}` yields
`WithConversationID` (flag plumbing without the e2e cost).

## 3. Grants in connect mode: ephemeral by default, durable by config (§1.2)

### 3a. Config surface

| Layer | Addition |
|-------|----------|
| `internal/config/config.go` `ConnectSection` | `EphemeralGrants *bool \`yaml:"ephemeral_grants"\`` — doc: "runtime-approved bash globs live in memory only (default true); false persists them to settings.json like serve mode" |
| `cmd/app/options.go` `cliConfig` | `ConnectEphemeralGrants bool` |
| `cmd/app/root.go` flags | `fl.BoolVar(&cfg.ConnectEphemeralGrants, "connect-ephemeral-grants", true, "...")` |
| `cmd/app/root.go` env | `TENZING_CONNECT_EPHEMERAL_GRANTS` (`strconv.ParseBool`; applied when flag unchanged, same block as the other connect env vars) |
| `cmd/app/configmerge.go` | `if f.Connect.EphemeralGrants != nil && !changed("connect-ephemeral-grants") && !present("TENZING_CONNECT_EPHEMERAL_GRANTS")` |
| `cmd/app/defaults/tenzing.yaml` | no `connect:` block exists today; leave as is |

### 3b. Behaviour

`cmd/app/connect.go`:

```go
// connectApprove answers a pending approval and applies an "allow always"
// glob: in memory for the process lifetime (ephemeral, the fleet default) or
// through the settings store (durable, serve-mode parity).
func connectApprove(store *app.BashAllowStore, registry *approvals.Registry, ephemeral bool, callID string, approved bool, glob string) {
    if approved && glob != "" && store != nil {
        if ephemeral {
            store.Rules().AllowPattern(glob)
        } else if err := store.Add(glob); err != nil {
            slog.Warn("connect: persist allow rule failed", "glob", glob, "error", err)
        }
    }
    answerApproval(registry, callID, approved)
}
```

Wire: `Approve: func(id string, ok bool, glob string) { connectApprove(cfg.BashAllow, registry, cfg.ConnectEphemeralGrants, id, ok, glob) }`.

Why this works: `applyBashRules` (root.go:115) runs before mode dispatch, so
`cfg.BashAllow` exists in connect mode; its `*BashRules` is the pointer held by
`PermissionPolicy.Bash` that the gate consults, so the grant is live for the
next call. `AllowPattern` is mutex-guarded, copy-on-write.

### 3c. Tests

- `cmd/app/connect_test.go` `TestConnectApprove` table: ephemeral ⇒ rule
  present, settings file absent; durable ⇒ rule present, file contains glob;
  `approved=false` + glob ⇒ nothing added; empty glob ⇒ nothing added.
- `TestConnectConfigMerge` gains an `ephemeral_grants: false` case;
  `TestMergeEnvConnect` gains the env var.

### 3d. Docs

- `PROTOCOL.md` §3 `approve` row: "`glob` adds an allow rule when `approved`. Serve mode persists it to settings.json; connect mode keeps it in memory unless `connect.ephemeral_grants: false`."
- `README.md` connect table: new `connect.ephemeral_grants` row; "Approvals are push" bullet gets one sentence.
- `SYSTEM_ARCHITECTURE.md` connect flags list + connect-mode paragraph.
- `TENZING_CHANGES.md` §1.2: rewrite premise per §0.

## 4. Graceful shutdown (§1.3)

### 4a. SIGTERM (independent one-liner, do first)

`cmd/app/main.go`: `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`.

ctx cancels → `client.Run` returns nil → `runConnect` returns → deferred
`h.Shutdown()` → exit 0. In-flight turn cancelled through the existing chain
(turnCtx ⊂ connCtx ⊂ ctx). Applies to all three modes.

### 4b. `shutdown` command (additive)

`PROTOCOL.md`:
- §3 row: `shutdown` `{id}` — "Cancel the running turn and queued ones, then exit. Reply: `result{outcome:"cancelled"}` for the running turn (if any), then `shutdown_ack{id}`, then a normal close; the process exits 0."
- §2 row: `shutdown_ack` `{id}`.
- §4 bullet: "Shutdown: `shutdown` is the deterministic teardown; SIGTERM is the equivalent without an ack. Neither reconnects."

`internal/app/wsclient/messages.go`: `Shutdown{Type, ID}` + `decode` case;
`ShutdownAck{Type, ID}`; package doc lists `shutdown`.

`internal/app/wsclient/client.go`:
- `Client.shutdownReq chan Shutdown` (buffered 1), `Client.shuttingDown atomic.Bool`.
- `executeQuery`: per-turn `turnDone` channel closed *after* the `Result` is enqueued.
- `dispatch` case `*Shutdown`: set `shuttingDown`; `cancelTurnContext()`; `c.h.Cancel()` (flushes the serial queue — no new handler); goroutine waits `turnDone` if a turn was running, then `shutdownReq <- *m`.
- Write pump: `case sd := <-c.shutdownReq:` → drain outbox non-blocking, write `ShutdownAck{ID: sd.ID}`, `conn.Close(StatusNormalClosure, "shutdown")`, `connCancel()`, return.
- `Run`: after `dialAndServe` returns, `if c.shuttingDown.Load() { return nil }`.
- `runConnect` returns nil → deferred `h.Shutdown()` (persister stopped, session store closed, session-end hooks) → exit 0.

Older agents drop the unknown type (§6); the plane falls back to SIGTERM.

Tests (`client_test.go`): `TestShutdownMidTurn` (result{cancelled} then
shutdown_ack, Run returns nil, no second hello), `TestShutdownIdle`,
`TestShutdownCallsCancelHandler`.

## 5. `result.files_touched` (§2.1)

Additive optional field. Paths from **successful** Read/Edit/Write calls
during the turn (main agent and subagents), deduplicated, in first-seen order.
Denied or failed calls touched nothing and are excluded.

`internal/app/wsclient`:
- `Result.FilesTouched []string \`json:"files_touched,omitempty"\``.
- `Handlers.RunTurn` returns a struct instead of a 3-tuple:
  `type TurnReport struct { Answer string; Err error; Denied int; FilesTouched []string }`.
  Touches `executeQuery`, `handlersFor`, and `connect.go`'s
  `connectRunFunc`/`connectSerialQueue.run`/`runConnectTurn`.

`cmd/app/connect.go`:
- Replace `denied atomic.Int64` with `turnStats{mu sync.Mutex; denied int; files []string; seen map[string]struct{}}` with `reset()` and `snapshot()`.
- `runConnectTurn` calls `stats.reset()` before `RunTurnWithImages` and builds the `TurnReport` from `stats.snapshot()` after. **Side effect, intended:** `denied_tools` becomes per-turn as PROTOCOL.md §2 specifies (today the counter only resets on disconnect, so it accumulates across turns).
- `observeConnectEvent`: on `core.ToolSucceededEvent` with `ToolName` in {Read, Edit, Write}, `json.Unmarshal(e.Input, &struct{ FilePath string \`json:"file_path"\` })` and record. Other tools ignored; malformed input ignored.
- `OnDisconnect` calls `stats.reset()` (replaces `denied.Store(0)`).

Tests: `cmd/app/connect_test.go` `TestObserveConnectEventFilesTouched`
(Read+Write same path ⇒ one entry; Edit ⇒ added; Bash ⇒ ignored; ToolFailed
Write ⇒ ignored); `TestTurnStatsResetPerTurn`. `wsclient`
`TestEventAndResultShapes` asserts `files_touched` serialises and is omitted
when empty.

Docs: `PROTOCOL.md` §2 `result` row gains `files_touched?` with the
definition above; `README.md`/`SYSTEM_ARCHITECTURE.md` one line each.

## 6. Doc drift sweep (same change, per CLAUDE.md)

| Doc | Update |
|-----|--------|
| `PROTOCOL.md` | §1 hello field · §2 `shutdown_ack`, `result.files_touched` · §3 `approve` semantics, `shutdown` · §4 shutdown bullet |
| `TENZING_CHANGES.md` | §0 corrections; §1.1/1.2/1.3/2.1 marked done; §3 item 2 → covered by `TestConnectResumeEndToEnd` |
| `README.md` connect section | `connect.ephemeral_grants` row; grants, shutdown/SIGTERM, files_touched sentences |
| `SYSTEM_ARCHITECTURE.md` connect section + flags list | same; `deps.llms` interface note in the cmd/app paragraph |
| `AGENTS.md` | `cmd/app` paragraph mentions `deps` — add "llms is an interface (test seam)"; wsclient row unchanged |
| `internal/config/config.go` docs, `internal/app/wsclient/messages.go` package doc | new key, new command |

## 7. Order and size

1. §4a SIGTERM → `go build ./...`
2. §2a LLM seam (interface only) → `go test ./cmd/app/`
3. §1 hello.conversation_id → wsclient tests
4. §5 `TurnReport` + files_touched (touches RunTurn signature — do before shutdown tests are written against it) → wsclient + cmd/app tests
5. §3 grants + knob → cmd/app tests, temp-file assertions
6. §4b shutdown → `go test -race ./internal/app/wsclient/`
7. §2b–c e2e (depends on shutdown for the clean run-1 exit) → `go test -race ./cmd/app/`
8. §6 docs → every `PROTOCOL.md` row has a Go struct and vice versa

Total ≈ 220 lines of code, ≈ 300 lines of tests. Final gate passed:
`gofmt -l . && go build ./... && go vet ./... && go test -race ./...`.
