# api

HTTP/SSE front end for one harness. A transport adapter: requests map onto harness operations, harness events stream back out as SSE. The application builds the harness and injects it; this package owns the listener, routes, and handlers, and composes four subpackages that each own one model the adapter orchestrates. Top-level (importable) package, but it imports `internal/...`, so only this module can build a harness to attach.

## Layout

| Package | Root | Job |
| --- | --- | --- |
| `api` | `Server` (`api.go`) | listener, route table, handlers, bus→SSE forwarding (`events.go`) |
| `api/turnqueue` | `Queue` | run one turn at a time; FIFO follow-ups; cancel drops the queue |
| `api/sse` | `Broadcaster` | fan events out to connected streams (drop-on-full); `Translate` maps bus events to wire envelopes |
| `api/approvals` | `Registry` | pending AskUser calls keyed by call ID |
| `api/costs` | `Tracker` | per-model token + USD accounting from `LLMResponseEvent`s |
| `api/ui` | `Handler(debug)` | the embedded single-page browser client (`index.go` holds the HTML) |

Dependency direction: `api` → subpackages. Subpackages import only `internal/core`, `internal/app/wire`, `internal/app/nexus` (event types), `internal/config`, `pkg/common`; none imports `api` or each other.

## Lifecycle

Two-phase, because the harness streams text/thinking deltas through options that must exist before it does:

```go
srv := api.New(api.ServerConfig{Bus: bus, Logs: logB, /* ... */})
h, _ := harness.New(llm,
    harness.WithEventBus(bus),
    harness.WithTextDeltaHandler(srv.TextDelta),
    harness.WithThinkingDeltaHandler(srv.ThinkingDelta),
    /* ... */)
srv.Attach(h)
errCh, err := srv.Start("127.0.0.1:8080") // ErrNotAttached before Attach
// ...
srv.Shutdown(ctx) // closes the queue (cancels the turn), the broadcaster (ends SSE streams), the listener
h.Shutdown(); bus.Close() // caller-owned, after Shutdown
```

`New` subscribes to the bus and starts `forwardEvents` immediately, so the cost tracker and approval registry see events from the first turn.

## Collaborators are interfaces

`ServerConfig` and `Attach` take consumer-side interfaces declared in `api.go`, satisfied implicitly by the concrete types `cmd/app` passes: `Harness` (the 14 harness methods the routes use; embeds `turnqueue.Runner`), `EventBus` (`Subscribe`, `Emit`), `Nexus` (`Read`, `WebhookHandler`), `LogStream` (`SSEHandler`), `AllowStore` (`Add`, `Rules`). Tests stub them without building a harness.

**Typed-nil hazard:** `Nexus` and `BashAllow` are optional. A nil `*nexus.Nexus` assigned to the interface field is a non-nil interface and the server will call through it. Leave the field unset when the pointer is nil (`cmd/app/container.go` does this).

Other `ServerConfig` fields: `Bus` (required), `Logs` (nil unmounts `GET /debug`), `OnTurnEnd` (nexus trigger flush hook), `Pricing`, `ResolveLLM`/`ModelNames` (closures for `POST /model` / `GET /models`; nil → 400 / empty list), `Cwd`, `TrustEnvDefault`, `Debug`.

## Files in `api/`

| File | Contents |
| --- | --- |
| `api.go` | `Server`, `ServerConfig`, collaborator interfaces, `ErrNotAttached`, `New`, `Attach`, `Start`, `Shutdown`, `TextDelta`/`ThinkingDelta`, `registerRoutes` (raw routes `GET /`, `GET /events`, `GET /debug`, `POST /ingest/{name}` bypass huma; everything else is a typed `route(...)` and appears in the OpenAPI spec), `runnerFunc` |
| `events.go` | `forwardEvents` + `observe`: subagent label map, approval capture, cost tracking, trailing `cost` event |
| `turns.go` | `/query` `/steer` `/state` `/cancel` `/info`, `validateImages`, `StartNexusTurn`/`nexusPrompt`, `contextWindow` |
| `approvals.go` | `/approve` (`takePending`, `persistAllow`), `/preview`, `/suggest` |
| `controls.go` | `/clear` `/resume` `/compact` `/thinking` `/model` `/models` `/stats` `/trust` (GET/POST) |
| `sessions.go` | `/sessions` (GET/DELETE/PATCH), `/messages` |
| `types.go` | huma input/output structs (no functions, no test file) |

Every other source file has a companion `_test.go`. `api_test.go` holds the shared doubles (`gatedAgent`, `answerAgent`, `stubLLM`, `safeRecorder`) and helpers (`newTestServer`, `submit`, `idle`, `waitFor`, `streamEvents`); handler tests that need no harness build `New(ServerConfig{})` and seed `srv.approvals` directly.

## SSE events

Harness events go through `sse.Translate` (wire envelope + `agent` label). Lifecycle events are published directly: `status` ({state, query}), `queued` ({query, position}), `answer` ({text}), `error` ({error}), `canceled` ({query}) from the turn queue; `cost` (running `costs.Stats`) after every `llm.response`; raw-text `text_delta` / `thinking_delta` from the delta callbacks.
