# System One models in `tenzing.yaml` — `models:` splits into `llm:` and `systemone:`

> **Follow-up:** this plan deliberately stopped at "a client can be built". The consumer
> side — `systemone_model:`, the `systemone:` block, and the three harness decisions that
> use them — is `../2026-09-20-systemone-harness/PLAN.md`, which is built. The decisions
> below stand as the record of *this* change's scope; read the follow-up for what the
> harness does with a System One client today.

## Context

`pkg/providers/protocols/systemone` now implements `common.SystemOne`, the decision
counterpart to `common.LLM`: one `state` plus a map of typed questions in, one
calibrated typed answer per question out. No text generation, no tool loop, no
streaming. See `pkg/providers/protocols/systemone/AGENTS.md` for the wire contract.

The config cannot express such a model today. `config.File.Models` is a flat
`[]ModelEntry` (`internal/config/config.go:84`), every entry carries LLM-shaped
fields (`context_window`, `max_response_tokens`, `vision`, `reasoning_effort`), and
`modelregistry.Factory` only ever builds a `common.LLM` (`factory.go:28`).

Goal: declare System One models beside LLM models, resolve them by alias the same
way, and build clients for them — without pretending they are chat models.

```yaml
models:
  systemone:
    - name: jev
      provider: openrouter-jev
      model_name: typesafe/jev-1.13
  llm:
    - name: main-model
      provider: ollama-cloud
      model_name: glm-5.3-flash
      context_window: 999424
      max_response_tokens: 32768
      vision: true
      reasoning_effort: high
```

Decisions:

- **`models:` accepts both shapes.** A YAML sequence is the legacy flat list and
  loads as `llm:`; a mapping takes the two keys. A custom `UnmarshalYAML` on the
  section is ~12 lines and spares every existing `tenzing.yaml` and most test
  fixtures. Revisit only if the dual shape ever confuses more than it saves.
- **New provider type `systemone`.** The endpoint is its own wire protocol
  (`POST <base>/v1/systemone`), not an OpenAI-compatible chat route, so it cannot
  ride `openai_compat`. The type is named after the **protocol**, like the existing
  three — not after TypeSafe, because two vendors serve it (below).
- **One alias namespace across both lists.** Refs are bare aliases (`model:`,
  `--model`, `advisor_model:`), so an alias appearing in both lists would be
  ambiguous. Validation rejects it.
- **System One entries carry three fields only** — `name`, `provider`, `model_name`.
  Jev takes no images and exposes no reasoning tier, and OpenRouter reports
  `supported_parameters: []` for it, so `vision:` and `reasoning_effort:` would be
  config that does nothing. `max_response_tokens:` is meaningless too: output is a
  fixed-size typed answer, and it is not billed.
- **The token budgets are constants, not config.** Nothing is sent on the wire and
  the endpoint enforces its own limits, so declaring them per entry buys nothing.
  They live in the protocol package as `systemone.MaxRequestTokens` (65536: state
  plus all questions) and `systemone.MaxStateQuestionTokens` (32768: state plus the
  single longest question), for a caller sizing a fan-out batch to plan against.
  Caveat recorded there and in the protocol's `AGENTS.md`: OpenRouter advertises
  32000 for `typesafe/jev-1.13`, half of the first figure, so a caller pointing
  elsewhere than TypeSafe plans against the smaller number. Revisit only if a caller
  needs the limit to vary per declared backend.
- **No `cost:` block yet.** `Registry.pricing` is keyed by the wire model name seen
  on `LLMResponseEvent` (`modelregistry.go:46`), and System One calls emit no such
  event. Priced usage needs an event first; that is a separate change.
- **Stop at "a client can be built".** Nothing in the harness consumes
  `common.SystemOne` yet. No top-level `systemone_model:` key, no tool, no routing
  feature — those are cheap to add once a caller exists, and speculative before.

### Two backends, one protocol

TypeSafe serves the endpoint directly, and OpenRouter mirrors the same route and
body (`POST https://openrouter.ai/api/v1/systemone`; the other candidate paths 404).
Only the base URL and the model id differ, and both are already client options —
`WithBaseURL` and `WithModel` — so this plan needs no protocol-package change.

| | TypeSafe | OpenRouter |
| --- | --- | --- |
| `url:` | `https://api.typesafe.ai` | `https://openrouter.ai/api` |
| `model_name:` | `jev-latest` / `jev-1.13.0` | `typesafe/jev-1.13` |
| Advertised context | 64k / 32k (the package constants) | 32000 |

Reaching OpenRouter for both protocols means **two provider entries**, one per
protocol, which is already the idiom (`name:` is a free label, `type:` is the
protocol):

```yaml
providers:
  - name: openrouter                   # chat models
    type: openai_compat
    url: https://openrouter.ai/api/v1
    api_key: "$OPENROUTER_API_KEY"
  - name: openrouter-jev               # System One
    type: systemone
    url: https://openrouter.ai/api
    api_key: "$OPENROUTER_API_KEY"
```

Note the differing depth: `openai_compat` takes the base **including** `/v1`, while
the System One client appends `/v1/systemone` itself. Copying the compat URL into a
`systemone` provider yields `/api/v1/v1/systemone` and a 404, which Step 1 catches
at startup instead.

## Step 1 — Schema (`internal/config/config.go`)

- `File.Models` becomes `ModelsSection{ LLM []ModelEntry; SystemOne []SystemOneEntry }`
  with `UnmarshalYAML` accepting a sequence (legacy → `LLM`) or a mapping.
- `SystemOneEntry{Provider, Name, ModelName string}`. No other fields (see Decisions).
- `ProviderTypes` gains `"systemone"` (`config.go:130`).
- `validate()` extends the existing model loop:
  - both lists, error prefixes `models.llm[i]` / `models.systemone[i]`;
  - `name`, `model_name`, `provider` required; provider must be declared;
  - alias uniqueness **across** both lists;
  - a `models.systemone:` entry must name a `type: systemone` provider, and a
    `models.llm:` entry must not — a startup error beats `ErrUnknownProvider` on the
    first request;
  - a `type: systemone` provider requires `url:` and rejects one ending in `/v1`,
    naming the fix.
- verify: `go test ./internal/config/` — both shapes, cross-kind duplicate alias,
  wrong-type provider each way, `/v1`-suffixed URL, legacy list still loads.

## Step 2 — Registry (`internal/app/modelregistry/modelregistry.go`)

- `ResolvedSystemOne{Name string; Provider config.Provider}` — deliberately not
  `common.ModelDefinition`, whose fields are all LLM-shaped. No defaulting step:
  there is nothing to default.
- `Build(providers []config.Provider, models config.ModelsSection)` — signature
  change; second index `systemOneByName`.
- `ResolveSystemOne(alias string) (ResolvedSystemOne, error)`. Alias only: inline
  `{...}` refs exist for `--model` and nothing passes a System One ref on the command
  line yet.
- `List()` groups the two kinds under headings; System One rows print name, provider
  and wire id (no ctx/max-tokens columns, which do not apply).
- verify: `go test ./internal/app/modelregistry/`, plus the `--list-models` output.

## Step 3 — Factory (`internal/app/modelregistry/factory.go`)

- `Factory.GetSystemOne(rs ResolvedSystemOne) (common.SystemOne, error)` →
  `systemone.NewClient(WithAPIKey(prov.APIKey), WithBaseURL(prov.URL), WithModel(rs.Name))`,
  cached on the existing (provider name, wire name) key shape.
- `buildLLM` gains a `case "systemone"` returning a named error — "%s is a System One
  model; it cannot serve --model" — instead of falling through to
  `ErrUnknownProvider`.
- `prov.Extra` stays ignored for this type, as it is for anthropic and ollama.
- verify: factory tests both directions, including the cache key.

## Step 4 — Command wiring (`cmd/app/deps.go`, `root.go`)

- `deps` gains `systemones systemOneSource` (interface mirroring `llmSource`, so
  tests can fake it) and `resolveSystemOne`.
- `buildDeps(providers, models config.ModelsSection, providerFlags)`; `root.go:63`
  passes `file.Models` unchanged.
- `--list-models` prints both groups.
- Nothing else. No new flags, no new top-level config keys.
- verify: `go test ./cmd/...`; `tenzing --list-models` by hand against a config
  declaring both kinds.

## Step 5 — Defaults and docs

- `cmd/app/defaults/tenzing.yaml`: new nested shape, plus a commented `systemone`
  provider + model pair showing both backends. The `# Types:` comment there currently
  lists vendors ("cerebras, lightning, openai, openrouter") rather than the three real
  types — fix it to `anthropic, ollama, openai_compat, systemone` while editing.
- `README.md` (6 `models:` snippets), `SYSTEM_ARCHITECTURE.md`, root `AGENTS.md`
  (the `pkg/common`, `pkg/providers/protocols/systemone/` and modelregistry bullets).
- `pkg/providers/protocols/systemone/AGENTS.md` says "`internal/app/modelregistry`
  never builds one" — this change makes that false; update it in the same commit.
- Existing ADRs under `docs/adrs/` stay as written.

## Step 6 — Tests

Table-driven, alongside the per-step checks:

- `internal/config`: the Load matrix above; a legacy flat list and the nested mapping
  producing identical `ModelsSection` values.
- `modelregistry`: both kinds resolve; cross-kind alias collision rejected at
  validation; `ResolveSystemOne` on an LLM alias errors and vice versa.
- `factory`: `GetSystemOne` builds and caches; `Get` on a `systemone` provider fails
  with the explanatory error.
- `cmd/app`: `--list-models` golden output with both groups; a config using the
  legacy list still starts.
- Full gate before declaring done: `go build ./...`, `go vet ./...`,
  `go test -race ./...`.
