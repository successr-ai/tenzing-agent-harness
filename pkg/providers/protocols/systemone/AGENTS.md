# systemone

Client for the **System One** evaluation endpoint, served by TypeSafe's Jev model family. Wire contract: <https://docs.typesafe.ai/api>. Two backends serve the same route and body — TypeSafe directly, and OpenRouter — so the endpoint is a protocol, not a vendor; see [Backends](#backends).

A System One model is not a chat model. It takes one *state* plus a map of typed *questions* and returns one typed, calibrated answer per question. No text generation, no tool loop, no streaming. So it implements **`common.SystemOne`**, the decision counterpart to `common.LLM`, and **not `common.LLM`** — it never reaches `ModelPort`. `internal/app/modelregistry` builds one through `Factory.GetSystemOne`, from a `models.systemone:` entry behind a `type: systemone` provider; `--model` and every other model ref names a chat model. Do not add a `common.LLM` adapter here; the shapes do not correspond.

The interface and every type it names (`State`, `Question`, `Answer`, `EvaluationRequest`, `EvaluationResponse`) live in `pkg/common/systemone.go`, next to `LLM`. Callers depend on that interface; this package is one implementation of it, and a second System One provider would be another package here. Unlike the rest of `pkg/common`, those types carry JSON tags: the System One wire shape is the vocabulary itself, so there is no separate conversion layer — only the request envelope and `usage` are mapped, in `systemone.go`.

## Files

| File | Job |
| --- | --- |
| `systemone.go` | `Client`, its options, `Evaluate`, and the private wire envelope |
| `systemone_test.go` | wire-shape round trip against `httptest`, error/retry table, model precedence, constructor validation |
| `pkg/common/systemone.go` | the `SystemOne` interface, the shared types, the `NewNoul`/`NewChoice`/`NewScore` constructors |

## Protocol

```http
POST <base>/v1/systemone
Authorization: Bearer <API_KEY>
Content-Type: application/json
```

`<base>` is `WithBaseURL`, defaulting to `DefaultBaseURL` (`https://api.typesafe.ai`). The client appends `/v1/systemone`, so the base stops **before** `/v1`.

```json
{ "state": "…", "model": "jev-latest", "questions": { "<id>": { "type": …, "instructions": …, "criteria": … } } }
```

```json
{ "model": "jev-1.13.0", "answers": { "<id>": { "type": … } }, "usage": { "input_tokens": 296, "output_tokens": 20 } }
```

Question ids are caller-chosen, never sent to the model, and echoed back as the `answers` keys.

The types are `common.Question` and `common.Answer`; build questions with `common.NewNoul`, `common.NewNoulWithCriteria`, `common.NewChoice` and `common.NewScore`.

| Type | `criteria` | Answer fields |
| --- | --- | --- |
| `noul` | optional `{"true": …, "false": …}` | `noul` (0–1). No confidence — the one probability is the whole answer. |
| `choice` | required map option→description, ≤255 options, `null` description allowed | `choice`, `probabilities` (sum 1), `confidence` |
| `score` | required ordered array of 2–10 level descriptions, low→high | `score` (probability-weighted, lands between levels), `legend`, `probabilities`, `confidence` |

`instructions`, each Choice option description, each Score level and Noul's `true`/`false` all accept a string, a JSON object, a JSON array, or null — TypeSafe calls that an `EntryType`, and structured descriptions (`{"what": …, "examples": […]}`, `{"summary": …, "signals": […]}`) are the documented fix when options or levels are confusable. A structured value's fields are referenced from the question text in backticks, the same way a nested state field is: ``Is the resume for the same person as `potential_duplicate`?``

Two consequences for the Go types: `Question.Instructions` and `Question.Criteria` are `any`, and **`Answer.Legend` is `map[string]any`, not `map[string]string`** — the legend echoes the criteria entries back, so structured levels come back as objects and a string-typed legend fails to decode the whole response. `TestStructuredScoreLevelsRoundTrip` pins that.

`usage.input_tokens` / `output_tokens` are optional on the wire; absent means zero here.

**Errors:** `401` bad key (OpenRouter answers 401 with `No cookie auth credentials found` when the header is missing entirely) · `422` validation (body names the field) · `429` rate limit · `529` overloaded. Non-2xx becomes a `*common.APIError` carrying the status and the response body as `Message`.

## Backends

| | TypeSafe | OpenRouter |
| --- | --- | --- |
| `WithBaseURL` | `https://api.typesafe.ai` (default) | `https://openrouter.ai/api` |
| `WithModel` | `jev-latest`, `jev-preview`, or a pinned `jev-1.13.0` | `typesafe/jev-1.13` |
| Key | `$TYPESAFE_API_KEY` | `$OPENROUTER_API_KEY` |
| Auth | `Authorization: Bearer` | same |
| Context | 64k per request, 32k state + longest question (the package constants) | 32000 reported, `max_completion_tokens` 28800 |
| Price | \$0.042 / Mtok input, output free | `prompt` 0.000000042/token, `completion` 0 — the same rate |

Same route, same body, same answer shapes; only the base URL and the model id differ, both of which are already client options. Nothing in this package is vendor-specific.

**The base-URL trap:** OpenRouter's OpenAI-compatible base is `https://openrouter.ai/api/v1`, and the natural instinct is to reuse it here. Don't — this client appends `/v1/systemone`, so that base produces `/api/v1/v1/systemone` and a 404. The System One base is `https://openrouter.ai/api`.

OpenRouter facts worth knowing when checking their side: the model is absent from the default `GET /api/v1/models` catalog (its modality is `text->decisions`, not a chat modality), so use `GET /api/v1/models/typesafe/jev-1.13/endpoints` instead — that is where the context, pricing and the dated endpoint tag (`typesafe/jev-1.13-20260917`) come from. `supported_parameters` is empty: there are no sampling knobs to pass, which is what a decision model looks like on a catalog built for chat models. Their model page is <https://openrouter.ai/typesafe/jev-1.13>.

## Usage

```go
var judge common.SystemOne

judge, err := systemone.NewClient(systemone.WithAPIKey(key))

resp, err := judge.Evaluate(ctx, common.EvaluationRequest{
    State: map[string]any{"message": text, "order_id": "A-104"},
    Questions: map[string]common.Question{
        "is_urgent":  common.NewNoul("Does this convey urgency?"),
        "department": common.NewChoice("Which team should handle this?", map[string]any{
            "billing":   "Payments, invoicing, refunds",
            "technical": "Bugs, outages, integrations",
        }),
        "frustration": common.NewScore("How frustrated is the customer?", "Calm", "Frustrated", "Very angry"),
    },
})

if resp.Answers["department"].Confidence < 0.5 { /* route to a human */ }
```

For OpenRouter, the same call plus two options:

```go
judge, err := systemone.NewClient(
    systemone.WithAPIKey(key),
    systemone.WithBaseURL("https://openrouter.ai/api"),
    systemone.WithModel("typesafe/jev-1.13"))
```

Take a `common.SystemOne` in your own code, not a `*Client` — same rule as `common.LLM`. Options: `WithAPIKey` (required — nothing in `pkg/` reads env vars), `WithBaseURL`, `WithModel`, `WithHTTPClient`, `WithRetryBackoff`. `EvaluationRequest.Model` overrides the client's model for one request; empty means the client's own. `Client` is safe for concurrent use.

Deliberately absent, matching the other protocol packages only where it earns its place: no `WithRateLimit`/`WithMaxConcurrency` wrap (`ratelimit.Wrap` decorates `common.LLM`, which this is not), no `GET /v1/models` call, no streaming. Add them when a caller needs them.

## Rules that shape calling code

- **Batch.** Jev ingests the state once and evaluates every question against it in parallel. Extra questions in one request are near-free; splitting them across requests is not. One `Evaluate` call, many questions.
- **Questions in a request are independent.** A question that consumes another's answer needs a second request.
- **Keep facts in state, judgments in questions.** Anything a decision must compare belongs in the same state.
- **Confidence is the second axis** (Choice and Score only): the answer says *what*, confidence says *whether to act*. Gate destructive actions higher than read-only ones. Thresholds are domain constants — keep them and the question text in one reviewable place, not scattered through call sites.
- **Nouls threshold in code.** 0.5 when a false yes and a false no cost the same; raise it when false positives are expensive.
- **Each question is a judgment a knowledgeable person makes in seconds.** Decompose a broad judgment into atomic questions and combine them in Go.
- **Ask literally.** Jev answers the question as written, not as meant: scoping words, negations and implied conditions are read at face value. When explaining a wrong answer means saying what you really meant, that explanation belongs in `instructions` or the criteria.
- **Keep arithmetic in code.** Counting, date ordering and distance, numeric magnitudes and anything derived from a Score's expected value are documented weak spots. Ask one question per candidate and sum the results in Go; extract date components as a Choice over enumerated values and compare them in code.
- **Filter the state.** Accuracy falls as the state fills with detail the decision does not need, and a large state makes a wrong answer hard to attribute. Send what the question needs.
- **Keep criteria and instructions aligned.** Contradictions between them, and multi-hop indirection ("a property of a property"), both cost accuracy.

Per-version failure modes live at <https://docs.typesafe.ai/model-jaggedness/jev-1.13> — check that page when moving to a new model version.

## Model facts (jev-1.13.0)

Text only — string, JSON object, or array of text values; encode anything else first. 64k tokens per request (state + all questions), 32k for state + the single longest question — the `MaxRequestTokens` / `MaxStateQuestionTokens` constants, which are documentation for callers sizing a batch, not something the client sends or enforces. OpenRouter advertises half the first figure (32000) for the same model; plan against the smaller one there. 250k tok/s, 1200 req/min against TypeSafe directly (adjusted without notice while they scale); OpenRouter meters separately under its own account limits. Input tokens billed, output free. English is strongest; other languages work worse — watch confidence.

`jev-latest` and `jev-preview` are aliases that move between releases. `Response.Model` reports the version that actually answered — log it. Pin a versioned id once thresholds are tuned against one. OpenRouter exposes no moving alias: `typesafe/jev-1.13` is already a pinned minor version, which is the behaviour you want anyway once thresholds are tuned.

## Retry

`Evaluate` retries every failure `(*common.APIError).Transient()` accepts — 408, 429, any 5xx (the endpoint answers `529` when overloaded), and a request that never got a response — which is the set TypeSafe's own SDKs retry (`{408, 429, 500–599}`). Delays come from `ratelimit.Backoff` with the `ratelimit.NewDefaultBackoff` values, so the backoff and its log line match the other protocols; `ratelimit.RetryOnRateLimit` is deliberately not used, because it retries 429 alone.

Not honored: `Retry-After` and `retry-after-ms`, which the endpoint may send on a 429 (`ponytail:` comment names the upgrade — a private error type carrying the header). Keep that comment and this section in sync.

## Changing this package

The doc pages this package mirrors are `/api`, `/models`, `/primitives*`, `/confidence` under <https://docs.typesafe.ai>, and their raw Markdown is fetchable by appending `.md`. OpenRouter's side is machine-readable at `GET https://openrouter.ai/api/v1/models/typesafe/jev-1.13/endpoints` (no auth needed). When the wire contract moves, update `pkg/common/systemone.go`, this file, and the `pkg/common` and `pkg/providers/protocols/systemone/` bullets in the root `AGENTS.md` in the same change.

Known doc drift: the interactive request examples embedded across the primitive pages show `"selectedModels": ["jev-latest"]`, an array. `/api`, `/models` and both official SDKs specify a single `"model"` string, which is what this client sends.
