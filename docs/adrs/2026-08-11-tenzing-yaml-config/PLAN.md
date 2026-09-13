# Unified Config File: `tenzing.yaml` (`internal/config`) — replaces models.yaml

## Context

Configuration is currently scattered: `models.yaml` (model registry only), six env vars
(`container.go:28`), CLI flags for everything else (`root.go:112-148`), prompt drop-ins
(`projectconfig.go`), `nexus.yaml`. No single file can express a full setup
(models + advisor + subagent + budgets + permissions).

Goal: **one** YAML file holding every durable setting, including the model registry.
`models.yaml` is **removed**, not merged with — its schema moves into `tenzing.yaml`
under `models:`. Precedence, highest first:

**CLI flag > env var > tenzing.yaml > default.**

Decisions:

- **Long flag only.** `-c` is taken by `--continue` (`root.go:137`); breaking that shorthand
  is not worth it. `--config` has no short form.
- **Default path `tenzing.yaml`**, like models.yaml behaved: flag `--config` >
  env `TENZING_CONFIG` > default `tenzing.yaml` (cwd-relative). Missing file at the
  *default* path is fine (builtins/flags only); missing file at an *explicitly given*
  path (flag or env) is a startup error. Without this, dropping models.yaml would force
  `--config` onto every run that today relies on ambient `models.yaml`.
- **models.yaml support removed entirely.** No fallback, no dual loading.
  `TENZING_MODELS_CONFIG` env var and the `ModelsConfig` field
  (`container.go:32`, `root.go:43,85`) are deleted. README gets a migration note
  (wrap existing content under `models:`, rename file).
- **Schema lives in `internal/config`**, a new package owning file shape + parsing +
  validation. No cobra/env knowledge there; the flag/env/file merge stays in `cmd/app`
  (it needs `cobra.Flags().Changed` and `os.LookupEnv`).
- **Durable settings only.** Per-run controls stay flag-only: `-p/--prompt`,
  `--output-format`, `--list-models`, `--resume`, `--continue`, `--conversation-id`,
  `--trust`, `--timeout`. Trust stays out of the file deliberately (security decision,
  already has `--trust` + `TENZING_PROJECT_TRUST`).
- **Strict parsing.** Unknown keys are a startup error (`yaml.Decoder.KnownFields(true)`) —
  a typo'd `advisor_modle:` must fail loudly, not silently no-op. Also catches a stale
  models.yaml passed as `--config` (top-level `default:` is not a tenzing.yaml key).

## File shape

```yaml
# tenzing.yaml — all keys optional
model: openrouter/some-model          # main model ref or inline {provider,name,...}
subagent_model: ""
blackboard_model: ""
advisor_model: openrouter/stronger    # enables advisor + write-gate, same as the flag
advisor_nudge: 0

max_tokens: 0                          # per-turn budgets
max_iterations: 0
max_wall_clock: "0s"                   # Go duration string
thinking_budget: 0                     # reasoning tokens per LLM call, 0 = provider default

subagent_depth: 1
approval_timeout: "120s"
no_permissions: false
dangerously_skip_permissions: false
read_only: false
thinking: false
no_session: false
no_context_files: false

system_file: ""                        # path, same as --system
base_url: ""
api_key: ""                            # supported; document that env vars are preferred

port: 8080
nexus_config: nexus.yaml
debug: false

mcp_servers:                           # structured, nicer than "name=cmd args"
  - name: fs
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

models:                                # absorbs models.yaml
  default: openrouter/some-model      # note: `model:` above wins over this
  entries:
    - provider: openrouter
      name: some-model
      context_window: 128000
      max_tokens: 32768
      base_url: ""
      vision: false
      cost: {input: 1.0, output: 3.0}
```

## Step 1 — `internal/config` package (new)

- `config.File` struct mirroring the YAML above. Durations as `string`, parsed to
  `time.Duration` at load with clear errors ("max_wall_clock: invalid duration ...").
- Model schema types move here from `cmd/app/models.go`: `ModelsSection`
  (`Default` + `Entries`), `ModelEntry`, `CostEntry` — exported, YAML-tagged.
- `config.Load(path string, explicit bool) (File, bool, error)`: read, strict-decode,
  parse durations, validate. Missing file: `explicit` → error; default path → zero File,
  found=false, nil error (mirrors today's models.yaml behavior, `models.go:133`).
- Present/absent tracking via **pointer fields for scalars** (`SubagentDepth *int`,
  nil = unset) — "file sets `subagent_depth: 0`" must be distinguishable from
  "file omits it" during merge.
- Tests: happy path, unknown key rejection, bad duration, explicit-missing vs
  default-missing, models section round-trip, empty file, stale-models.yaml shape
  rejected with a helpful error.

## Step 2 — Model registry from tenzing.yaml (`cmd/app/models.go`)

- `modelsFile`/`modelEntry`/`costEntry` deleted; `defFromEntry` and registry logic keep
  working against the `internal/config` types.
- `loadModelRegistry(path)` file-reading is deleted; replaced by
  `buildRegistry(config.ModelsSection)` — pure, no I/O. Root wiring passes the loaded
  file's `models:` section (zero section → builtins only, exactly today's
  missing-models.yaml behavior).
- Default-model chain (replaces `root.go:65-72` chain):
  `--model` flag > `TENZING_MODEL` > tenzing.yaml `model:` > tenzing.yaml
  `models.default` > compiled-in.
- `--list-models` reflects the tenzing.yaml registry (config load precedes the
  ListModels branch in RunE).
- `Config.ModelsConfig` / `TENZING_MODELS_CONFIG` removed from `container.go`.

## Step 3 — `--config` flag + merge (`cmd/app/root.go`, `options.go`)

- `fl.StringVar(&cfg.ConfigPath, "config", "", "YAML config file (default tenzing.yaml, env TENZING_CONFIG); CLI flags and env vars override its values")`.
- Path resolution in RunE: flag > `TENZING_CONFIG` > `"tenzing.yaml"`; `explicit` =
  flag passed or env set. Load before model resolution and before the ListModels branch.
- `mergeConfigFile(cfg, file, cmd.Flags().Changed, envPresent)` — same shape as the
  existing `mergeEnv`. Per field: apply file value only when the flag wasn't passed
  AND (for the env-backed settings: `SERVER_PORT`, `LOG_DEBUG`, `NEXUS_CONFIG`,
  `TENZING_MODEL`) the env var isn't present.
- **Set-flag subtlety**: `SubagentDepthSet` / `ApprovalTimeoutSet` / `ThinkingSet` must
  become true when the file supplies those keys (nil-check on the pointer), otherwise
  `harnessOptions` ignores the value (`options.go:139-155`).
- `mcp_servers` file entries land as pre-parsed `[]mcp.ServerConfig` in a new cliConfig
  field consumed by `harnessOptions` alongside the `--mcp-server` strings (both apply;
  they're additive, not overriding).
- `api_key`/`base_url` from file flow into `llms.baseURL`/`llms.apiKey` under the same
  precedence (flag wins).

## Step 4 — Docs

- Flag help text; README config-file section with the full annotated example **plus a
  models.yaml migration note** (nest under `models:`, `models:` list key becomes
  `entries:`, delete `TENZING_MODELS_CONFIG`).
- `AGENTS.md` (root + `cmd/app` if one exists) and `SYSTEM_ARCHITECTURE.md`: new package,
  precedence chain, models.yaml removal.
- New `internal/config/AGENTS.md` if sibling packages have one (check convention).

## Step 5 — Tests

Table-driven:

- `internal/config`: Load matrix (Step 1 list).
- Merge precedence: for a representative field of each kind —
  flag+env+file → flag wins; env+file → env wins; file only → file wins; nothing → default.
  Include one *Set-field case (file sets `subagent_depth` → option applied).
- Registry: entries from `models:` resolve; default chain order; zero section = builtins.
- Root-level: `--config` with full file drives print-mode config end-to-end;
  explicit `--config missing.yaml` → error; absent default `tenzing.yaml` → clean run
  on builtins.
- Existing models.yaml tests in `models_test.go` rewritten against `buildRegistry` +
  `config.Load`.

## Risks

- **Breaking change**: existing `models.yaml` files silently stop applying (default-path
  file no longer read). Mitigated by the README migration note; considered acceptable —
  pre-1.0 tool, user-directed. If softer landing wanted later: warn at startup when a
  `models.yaml` exists in cwd and no `tenzing.yaml` does.
- **Three-way merge sprawl**: ~25 fields × explicit merge lines. Verbose but flat,
  greppable, matches the existing `mergeEnv` idiom; reflection would be shorter and much
  harder to debug. Accepting verbosity.
- **api_key in a file on disk**: legitimate footgun; docs say prefer env/`--api-key` with
  shell expansion. Not blocking — users who write it chose to.
- **Schema drift**: every new flag now has two-to-three homes (flag, maybe env, file).
  Mitigation: `internal/config` AGENTS.md note + the merge function's shape makes a
  missing entry obvious.

## Verification

1. `go build -o /dev/null ./... && go test ./... && go test -race ./...`
2. No `tenzing.yaml`, no flags: behavior identical to today minus models.yaml
   (builtins only; existing root tests adjusted for the removed env var stay green).
3. Full `tenzing.yaml` (advisor + custom model + budgets), no other flags:
   `--list-models` shows the custom model; print run uses it; advisor gate active.
4. Same file + `--model <other>`: flag wins; + `TENZING_MODEL` set: env wins over file.
5. Explicit `--config nope.yaml` → clear startup error; unknown key → error naming it;
   old models.yaml passed as `--config` → strict-parse error (not silent misread).
