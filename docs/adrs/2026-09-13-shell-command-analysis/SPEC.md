# SPEC: AST-Based Bash Command Analysis for Permission Gates

Self-contained execution spec — assumes **no prior conversation context**. Symbols are named so they can be searched; line numbers are omitted because the branch is mid-refactor.

## Ground rules (from repo `CLAUDE.md` / root `AGENTS.md` — read both first)

- Update every `AGENTS.md`/doc statement your change makes untrue **in the same change**. This change touches the permissions layer, the bash tool, `settings.json`, the approvals API, the embedded UI, connect-mode protocol, and read-only mode. Root `AGENTS.md` (Permissions paragraph, Setting precedence paragraph, "Allow always" paragraph, `cmd/app` paragraph), `api/AGENTS.md`, `docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md`, and `SYSTEM_ARCHITECTURE.md` will need edits.
- Do not commit or push — repo owner handles version control.
- No stray build artifacts (compile-check with `go build -o /dev/null ./...`). `go mod tidy` after adding the dependency; `go.sum` changes are expected.
- Table-driven Go tests; run `go test ./...` and `go test -race ./...`.
- Surgical changes; match existing style. Mark deliberate simplifications with a `ponytail:` comment naming the ceiling.

## Problem

The bash tool's permission layer (`internal/features/permissions/bash.go`) decides per command using a hand-rolled splitter (`splitExpressions`) plus user-maintained allow/deny globs. Three gaps:

1. **Every command not covered by a glob prompts**, including pure read pipelines (`grep … | head`). The 23-command corpus in `corpus_test.go` is entirely read-only and every one of them prompted. Users converge on ~15 globs per project by clicking "allow always" repeatedly.
2. **The splitter is a hand-rolled approximation of shell syntax.** It handles quotes, `$(…)`, backticks, and the common separators, but not here-docs, process substitution, compound commands (`for`/`while`/`if`/`{ }`/`( )`), function definitions, `&>`/`>|` redirects, or bash `[[ ]]`. Each gap is a place a command can hide from a deny glob or be mis-split for an allow glob.
3. **Only two approval tiers exist in serve mode**: per-call approve and "allow always" (persisted). There is no session-only grant, though connect mode already has one (`ConnectEphemeralGrants`).

The user wants: parse commands with a real shell parser (`mvdan.cc/sh/v3`), break them into constituent simple commands, classify each for the resources it mutates (filesystem write/delete, network, VCS state), and gate on that: read-only commands run without a prompt, mutating ones prompt unless a rule covers them, and the approval UI shows *why* a command is being asked about.

## Background

### Existing machinery (verified in code)

- **Decision lattice** — `core.Decision` in `internal/core/extension.go`: `Allow < AskUser < Deny`. Hooks only escalate; `permissions.Ext.OnToolCall` sets `tcc.Decision`/`tcc.Reason` when its decision is higher.
- **Name-level policy** — `permissions.Policy`/`DefaultPolicy()` (`permissions.go`): `bash` is in `Ask`. `Policy.Bash *BashRules` refines that.
- **Command-level rules** — `BashRules` (`bash.go`): `allow`/`deny` `[]string` globs under an `RWMutex`, copy-on-write. `Verdict(command) (core.Decision, ok bool)`: split → strip `NAME=value` prefixes → deny pass (raw and stripped) → allow pass (every expression must be covered). `matchGlob` is a custom `*`/`?` matcher (not `path.Match`); `matchAny` treats a trailing ` *` as also matching the bare command. **Keep `matchGlob`/`matchAny` unchanged.**
- **Suggest** — `(*BashRules).Suggest(command) (glob, reason)` (`suggest.go`): first expression not covered by the live allow list; `writesFile` (redirect/tee detection) refuses to propose a glob for file writes; `globFor` keeps a subcommand (`subcommandTools`) or a leading flag (`flagSensitiveTools`, sed only).
- **Corpus pin** — `corpus_test.go` asserts the glob proposed for each of 23 real commands and that accepting each suggestion in turn silences the corpus on 15 rules.
- **Settings file** — `cmd/app/settingsfile.go` loads `{"permissions": {"bash": {"allow": [...], "deny": [...]}}}` via `BashRules.UnmarshalJSON`. `app.BashAllowStore.Add(pattern)` (`internal/app/bashallow.go`) persists via `writeBashSection(path, allow, deny)` **using `s.rules.Lists()` as its source** — so anything appended in memory would be written to disk on the next persist. This matters for the new session tier (see Design §6).
- **Approval flow (serve)** — `core.ApprovalRequestedEvent{CallID, ToolName, Input, Reason, Respond}` (`internal/core/approval.go`) → `api/events.go` captures into `approvals.Registry` as `Pending{Respond, Tool, Input}` → UI renders buttons (`api/ui/index.go`, search `'allow always'`) → `POST /approve {call_id, approved, allow?}` (`api/approvals.go` `handleApprove`; `approveInput` in `api/types.go`) → `persistAllow` → `store.Add` → `Respond`. `POST /suggest {call_id}` → `handleSuggest` → `Rules().Suggest`. **The UI does not currently render `reason`** for approval requests.
- **Approval flow (connect)** — `cmd/app/connect.go` `connectApprove(store, registry, ephemeral, callID, approved, glob)`: when `ephemeral` (default true, `--connect-ephemeral-grants`) it calls `store.Rules().AllowPattern(glob)` only; else `store.Add(glob)`. Protocol: `approve {id, call_id, approved, glob?}` in `PROTOCOL.md`, version "1". `approval_request {turn_id, id, tool, input}`.
- **Wire** — `internal/app/wire/wire.go` maps `core.ApprovalRequestedEvent` → `approvalRequested{CallID, ToolName, Input, Reason}`. Additive fields do not bump `wire.Version`.
- **Read-only mode** — `internal/harness/readonly.go` `readOnlyExt.OnToolCall`: allows `spawn_agent` and tools where `classify(name)` (late-bound `composite.ReadOnly`) is true; denies everything else including all `bash`. Installed instead of the permissions ext (`internal/harness/harness.go`, `case o.readOnly`), approval timeout forced to 0.
- **Extension order** — permissions (or read-only) first, then tool-call gate, then advisor gate, then the rest. Unchanged by this spec.
- **Bash tool** — `internal/features/builtins/tool_bash.go` runs `exec.CommandContext(tctx, "sh", "-c", command)`.

### mvdan.cc/sh/v3 (verified against v3.14.1, Go 1.25)

- Import `mvdan.cc/sh/v3/syntax`. `syntax.NewParser(syntax.Variant(syntax.LangBash))`; `p.Parse(strings.NewReader(cmd), "") (*syntax.File, error)`. `syntax.IsIncomplete(err)` is true for unterminated quotes/heredocs.
- `syntax.Walk(node, func(syntax.Node) bool)` visits everything, including command substitutions inside double quotes and here-doc bodies, process substitutions, and compound-command bodies.
- Nodes of interest: `*syntax.CallExpr{Assigns []*Assign, Args []*Word}` (simple command; `len(Args)==0` means assignment-only); `*syntax.Stmt{Cmd, Redirs []*Redirect, Background, Negated}`; `*syntax.Redirect{Op RedirOperator, N *Lit, Word *Word, Hdoc *Word}`; `*syntax.BinaryCmd{Op}` (`AndStmt`, `OrStmt`, `Pipe`, `PipeAll`); `*syntax.CmdSubst`, `*syntax.ProcSubst`; compound: `Subshell`, `Block`, `IfClause`, `ForClause`, `WhileClause`, `CaseClause`, `FuncDecl`, `DeclClause` (`export`/`declare`/`local`/…), `TestClause` (`[[ ]]`), `ArithmCmd`, `LetClause`, `TimeClause`, `CoprocClause`.
- Redirect operators: `RdrOut` `>`, `AppOut` `>>`, `ClbOut` `>|`, `RdrAll` `&>`, `AppAll` `&>>`, `DplOut` `>&` (both `2>&1` and bash's `>& file`), `RdrInOut` `<>`, plus input forms `RdrIn`, `DplIn`, `Hdoc`, `DashHdoc`, `WordHdoc`.
- **`(*Word).Lit()` returns `""` for any word that is not purely literal parts** — `'*.go'`, `"rm -rf x"`, `$FOO`, `$(…)` all yield `""`. A static-value walker over `Word.Parts` is required: `*Lit` → `Value`; `*SglQuoted` → `Value`; `*DblQuoted` → concatenation of its parts if all are `*Lit`; `*ParamExp`, `*CmdSubst`, `*ArithmExp`, `*ProcSubst`, `*ExtGlob`, `*BraceExp` → non-static.
- `syntax.NewPrinter().Print(w, node)` re-emits source for any node (used to produce the text globs match against). It normalises whitespace but preserves quoting.
- Dialects: `LangBash`, `LangPOSIX`, `LangMirBSDKorn`, `LangBats`. **There is no zsh dialect.**

### Why this design (decisions final — do not relitigate)

- **Replace the splitter, keep the globs, add a classifier.** `splitExpressions`, `scanDoubleQuoted`, `matchParen`, `matchByte`, `stripEnvPrefix`, `cutAssignment`, `isNameStart`, `isNameByte` and `writesFile` are deleted; the AST provides all of that. Glob allow/deny lists stay exactly as they are — they are the user's explicit overrides and the persistence/UI machinery around them already works. Classification fills the gap *below* the globs.
- **Parse as Bash; switch the tool to `bash -c`.** Bash is a superset of POSIX sh, so nothing that ran under `sh -c` is rejected by the parser. Switching the executor makes parser and runtime agree exactly (no `sh`-is-`dash` surprises with `[[ ]]`, arrays, `&>`). Zsh is out: mvdan has no zsh parser, and the tool never ran zsh. Zsh-only syntax hits the unparseable path (Ask).
- **Fully read-only commands auto-Allow.** This is the point of the feature. Correctness depends on the knowledge table being conservative — every entry that is not certain is Unknown, and Unknown never auto-allows.
- **Unknown = mutating → Ask.** Not Deny: the model must still be able to run project scripts after a human nods.
- **Taxonomy: `read`, `fs:write`, `fs:delete`, `net`, `vcs`, plus internal `unknown`.** No `proc`/`sys` class in v1: `kill`, `systemctl`, `launchctl`, `brew install`, bare `sudo -i` etc. fall to Unknown → Ask. `sudo <cmd>` is a wrapper and classifies by its inner command.
- **Classes are a bitset, not an ordered severity.** A segment can be `net|fs:write` (`git clone`). Category rules test set membership: deny if any class is denied, covered if every class is allowed. This avoids inventing a severity order between `net` and `fs:write`.
- **Recurse into wrappers and literal string-shells.** `sudo`, `env`, `time`, `nice`, `nohup`, `timeout`, `xargs`, `find -exec` unwrap to the inner command; `bash -c "<literal>"`, `sh -c`, `eval <literal…>` re-parse the literal. A non-literal string (`bash -c "$X"`) is Unknown. `ssh host cmd`, `docker exec`, `kubectl exec` do **not** recurse — they are classified by the table (remote execution is `net`; what runs remotely is not this host's concern).
- **No path scoping in v1.** `rm ./x` and `rm /etc/x` are both `fs:delete`. Static targets are listed in the approval reason for the human. Inside/outside-cwd rules are a follow-on.
- **Per-session tier is glob-scoped and in-memory.** Same glob as "allow always", never written to disk. Category-scoped session grants are a follow-on.
- **Unparseable → Ask** with the parse error in the reason. Deny globs are still matched against the whole raw command string as a last line (`rm -rf /` with an unbalanced quote elsewhere must still hit a `rm *` deny).
- **Knowledge table is Go code with `settings.json` overrides.** Compiled-in defaults; `bash.classify` lets a user add/override entries without a rebuild.
- **`--read-only` mode lets read-classified bash through.** Uses the built-in table only (no `settings.json` overrides — unattended mode should not widen from a file the operator may not have reviewed). Parse error or any non-`read` segment → Deny, as today.
- **Precedence (per command):** name-level Deny > parse error (Ask; deny globs on raw string) > glob deny (any segment) > glob allow (per segment, persisted or session) > category deny (any *uncovered* segment) > category allow + read auto-allow (per segment) > AskUser. A glob-allowed segment is exempt from category deny — the user wrote that glob deliberately.
- **Analysis reaches the UI through `Reason`.** No new fields on `ApprovalRequestedEvent`, `Pending`, or the wire envelope: the classifier's human-readable summary goes into `tcc.Reason`, which already flows to `ApprovalRequestedEvent.Reason` → `approvalRequested.reason` → SSE/WebSocket. The UI starts rendering it. ponytail: structured analysis on the wire is a follow-on if the control plane wants to act on it.

## Design

### 1. New package `internal/features/permissions/shell`

Pure functions, no `core` import. Depends only on `mvdan.cc/sh/v3/syntax` and stdlib. Files:

| File | Contents |
|---|---|
| `shell.go` | `Class`, `Segment`, `Analysis`, `Analyze(command string, t Table) Analysis` |
| `walk.go` | AST walk → segments: `CallExpr` handling, redirect attachment, static-word helper, `Printer`-based text |
| `wrappers.go` | wrapper unwrapping (`sudo`, `env`, `xargs`, `find -exec`, …) and string-shell recursion (`bash -c`, `eval`) |
| `table.go` | `Table` type, `Lookup`, `Builtin()` default table, `Merge(overrides map[string]Class)` |
| `shell_test.go`, `wrappers_test.go`, `table_test.go` | table-driven |

#### Types

```go
type Class uint8

const Read Class = 0 // no bits set

const (
    FSWrite  Class = 1 << iota // 1
    FSDelete                    // 2
    Net                         // 4
    VCS                         // 8
    Unknown                     // 16
)

func (c Class) IsRead() bool          // c == 0
func (c Class) Has(o Class) bool
func (c Class) String() string        // "read" | "fs:write,net" | … (sorted, comma-joined)
func ParseClass(s string) (Class, error) // inverse; "unknown" is NOT accepted from config

type Segment struct {
    Text     string   // simple command as printed by syntax.Printer, WITHOUT leading assignments — glob allow/deny match this
    Raw      string   // same, WITH assignments — glob deny additionally matches this
    Argv     []Arg    // static analysis of the words; Argv[0] is the command word
    Class    Class
    Why      []string // "rm: deletes files", "redirect > out.txt", "unknown command ./build.sh"
    Depth    int      // 0 top-level; +1 per $(…) / `…` / <(…) / bash -c / eval
}

type Arg struct {
    Value  string // static value, or "" when !Static
    Static bool
}

type Analysis struct {
    Segments []Segment
    Err      error // parse error; Segments is empty when set
}

func (a Analysis) Class() Class      // OR of all segment classes
func (a Analysis) Summary() string   // one line per non-read segment: "fs:delete  rm -rf x  (rm: deletes files)"; "read-only" when all read; "parse error: <err>" when Err
```

#### `Analyze(command string, t Table) Analysis`

1. `syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(...)`. On error return `Analysis{Err: err}`.
2. `syntax.Walk` the file. Maintain a depth counter: entering `*CmdSubst`/`*ProcSubst` increments for the subtree (Walk's callback returning `true` descends; track with a small stack keyed on node `End()` positions, or do a manual recursive walk — either is fine; manual recursion over `Stmt` is simpler for attaching redirects).
3. For each `*syntax.Stmt`:
   - If `Cmd` is `*CallExpr` with `len(Args) > 0`: build one segment (§ Segment construction).
   - If `Cmd` is `*CallExpr` with `len(Args) == 0`: assignment-only; **no segment** (matches current "executes nothing" behaviour). Still walk assignment values for `$(…)`.
   - If `Cmd` is `*DeclClause`, `*TestClause`, `*ArithmCmd`, `*LetClause`: no segment (shell-internal, `read`). Walk children for substitutions.
   - If `Cmd` is `*FuncDecl`: no segment for the declaration; walk the body normally (its commands become segments). A later call to the function name is an ordinary `CallExpr` → looked up in the table → Unknown. This is conservative and correct: the body's segments are already classified, and the call itself needs a glob or a nod.
   - Compound (`Subshell`, `Block`, `IfClause`, `ForClause`, `WhileClause`, `CaseClause`, `TimeClause`, `CoprocClause`, `BinaryCmd`): walk children.
   - **Redirects** on this `Stmt` (`Stmt.Redirs`): classify each (§ Redirects). If `Cmd` is a `CallExpr` segment, OR the redirect class into that segment and append to `Why`. If `Cmd` is compound and any redirect is a write, synthesise a segment `{Text: printer(stmt), Argv: nil, Class: FSWrite, Why: ["redirect > f on compound command"]}`.
4. Return segments in source order (order is informational; `Verdict` does not depend on it).

#### Segment construction (from a `*CallExpr`)

- `Argv`: for each `Word`, `staticValue(word) (string, bool)` per the walker described in Background. Note `Lit()` alone is insufficient.
- `Text`: `Printer.Print` of a copy of the `CallExpr` with `Assigns` cleared. `Raw`: `Printer.Print` of the original. (Printer normalises whitespace: `grep  -n` → `grep -n`. Existing globs match either; document in `AGENTS.md`.)
- Command word: if `!Argv[0].Static` → `Class = Unknown`, `Why = ["non-literal command word"]`, stop. Else `bin := Argv[0].Value`; if it contains `/` and does not start with `./`, `../`, `/` treat as-is; if it starts with `/` or `./` or `../` and the base name is in the table, **do not** use the table (`./ls` is not `ls`) → Unknown. Only bare names (`ls`, `git`) and absolute paths under `/bin`, `/usr/bin`, `/usr/local/bin`, `/opt/homebrew/bin` resolve to their base name. ponytail: fixed prefix list; extend when a real corpus command needs it.
- Wrapper check (§2) before table lookup.
- Table lookup (§3) → `Class`, `Why`.
- Redirects OR'd in afterwards.

#### Redirects

| Op | Classification |
|---|---|
| `RdrOut` `>`, `AppOut` `>>`, `ClbOut` `>\|`, `RdrAll` `&>`, `AppAll` `&>>`, `RdrInOut` `<>` | `fs:write` unless static target is `/dev/null`, `/dev/stdout`, `/dev/stderr`, `/dev/tty`, or matches `/dev/fd/N`. Non-static target (`> $OUT`) → `fs:write` with `Why` "redirect to non-literal path". |
| `DplOut` `>&` | If target word is all digits or `-` (`2>&1`, `>&2`, `>&-`) → read. Otherwise bash treats `>& file` as `&> file` → `fs:write` (same `/dev/*` exemption). |
| `RdrIn`, `DplIn`, `Hdoc`, `DashHdoc`, `WordHdoc` | read. Here-doc bodies (`Redirect.Hdoc`) are still walked for `$(…)` when the delimiter is unquoted (the parser already leaves the body literal when quoted). |

### 2. Wrappers and string-shells (`wrappers.go`)

Wrappers classify by their *inner* command. `Text`/`Raw` remain the full printed command (so a user glob `rm *` does **not** cover `sudo rm`; a glob `sudo rm *` does). `Why` gets a prefix like `"via sudo"`.

| Wrapper | Inner argv starts after |
|---|---|
| `sudo`, `doas` | flags; `-u USER`, `-g GROUP`, `-C N`, `-h HOST`, `-p PROMPT` consume a value. `sudo -i`/`-s` with no inner → Unknown. `sudo -e FILE` (sudoedit) → `fs:write`. |
| `env` | flags (`-i`, `-u NAME`, `-C DIR`, `-S`) and `NAME=value` tokens. `env` with no inner → read. |
| `command`, `builtin`, `exec`, `nohup`, `time`, `caffeinate` | leading flags |
| `nice` | `-n N` / `-N`; `ionice` `-c N -n N` |
| `timeout` | flags (`-s SIG`, `-k DUR`, `--preserve-status`, `--foreground`) then DURATION then inner |
| `stdbuf` | `-i/-o/-e MODE` |
| `xargs` | flags (`-0`, `-r`, `-t`, `-p`, `-n N`, `-P N`, `-I R`, `-L N`, `-s N`, `-d D`, `-a FILE`, `-E EOF`, long forms). `xargs` with no inner → inner is `echo` → read. |
| `find` | `find` itself is read. Scan args: each `-exec`/`-execdir`/`-ok`/`-okdir` starts an inner argv running to the next `;`/`\;`/`+` token; `-delete` → `fs:delete`; `-fprint`/`-fprint0`/`-fprintf`/`-fls FILE` → `fs:write`. Multiple `-exec` clauses each contribute. |

Non-static tokens in the wrapper's own flag region (e.g. `sudo $FLAGS rm x`) → Unknown.

String-shells re-parse a literal and splice the resulting segments in at `Depth+1`:

| Form | Rule |
|---|---|
| `bash`, `sh`, `zsh`, `dash`, `ksh` with a flag cluster containing `c` (`-c`, `-lc`, `-ec`, `-xc`, `--`? no) | Next static arg is the script → `Analyze(script, t)`; non-static → Unknown. Remaining args are `$0 $1…` and ignored. Same binary with no `-c` (`bash script.sh`, bare `bash`) → Unknown. |
| `eval` | All args static → join with `" "` → `Analyze`. Any non-static → Unknown. |
| `source`, `.` | Unknown (file contents are not available to static analysis). |

Recursion cap: `Depth > 4` → Unknown with `Why` "nesting too deep". A parse error inside a nested script → that segment is Unknown with the error in `Why` (the outer analysis does not fail).

### 3. Knowledge table (`table.go`)

```go
type Table map[string]Class // key forms below
func Builtin() Table
func (t Table) Merge(overrides map[string]Class) Table // returns a new map; overrides win
func (t Table) Lookup(argv []Arg) (Class, []string)
```

Key forms and lookup order for `argv` with base name `bin`:

1. **Flag keys** `"<bin> -x"` / `"<bin> --long"`: for every arg starting with `-` (before any `--` terminator): a long flag `--in-place=.bak` is looked up as `--in-place`; a short cluster `-ni.bak` is expanded per character (`-n`, `-i`, `-.`, `-b`, …) and each looked up. Every hit is OR'd in. (False positives are toward Unknown/write, never toward read; `sed -es/i/j/` flags as `fs:write` — acceptable.)
2. **Subcommand key** `"<bin> <sub>"` where `sub` is the first arg not starting with `-` (after skipping values of known value-taking flags: `git -C DIR`, `git -c K=V`, `go -C DIR`, `docker -H`, `kubectl -n NS`, `npm --prefix`). ponytail: fixed list, extend when a corpus command misclassifies. If found, OR in.
3. **Binary key** `"<bin>"` if neither a subcommand key matched (a subcommand tool's bare key should be absent or `Unknown` so `git foo` is Unknown).
4. Nothing matched → `Unknown`, `Why = ["unknown command <bin>"]`.

Some entries need argument-shape rules that a flat key cannot express; implement these as a small `special map[string]func(argv []Arg) (Class, bool)` consulted before the table (ponytail: a handful of closures, not a rule DSL):

- `git branch`/`git tag`/`git stash`/`git remote`/`git worktree`: read when every arg is a flag or (for `stash`) the sub-sub is `list`/`show`, (for `remote`) `-v`/`show`/`get-url`, (for `worktree`) `list`; otherwise `vcs` (`git remote add` → `net|vcs`).
- `git config`: read with `--get`, `--get-all`, `--get-regexp`, `-l`, `--list`; otherwise `vcs`.
- `tar`: flag cluster containing `x` or `c` → `fs:write`; `t` only → read; `-f -`/`-O` stays as computed.
- `tee`: `fs:write` unless every non-flag arg is `/dev/null`/`/dev/stderr`/`/dev/stdout`.
- `unzip -l`, `zipinfo` → read; other `unzip` → `fs:write`.
- `awk`/`gawk`/`mawk`: read unless any static arg contains `>` or `system(` → Unknown. ponytail: heuristic on the program text; the alternative is treating every awk as Unknown, which kills the corpus.
- `rsync`: `fs:write`; any arg containing `:` before a `/` (remote spec) or starting with `rsync://` → also `net`.
- `curl`: `net`; `-o`/`-O`/`--output`/`--remote-name` → also `fs:write`. `wget`: `net|fs:write` unless `-O -`/`--spider`/`-q -O /dev/null` → `net`.
- `python*`/`node`/`ruby`/`perl`/`php`: Unknown (arbitrary code), **including** `-c`/`-e` inline programs — not shell, cannot analyse. `perl -i`/`-pi` → `fs:write|unknown`.

Initial `Builtin()` content (add to, do not shrink; keep alphabetised in code):

- **read**: shell builtins (`cd pushd popd echo printf pwd true false : test [ type alias unalias set shift export declare local readonly unset read return break continue exit wait jobs trap ulimit umask mapfile readarray hash help times getopts`), `ls dir cat head tail less more grep egrep fgrep rg ag ack wc sort uniq cut tr sed(no -i) awk(heuristic) find(no -exec/-delete) fd locate which whereis whatis type file stat du df date cal env printenv id whoami hostname uname arch nproc dirname basename realpath readlink diff cmp comm patch(--dry-run only; else fs:write) jq yq xxd od hexdump strings column paste nl seq tac rev fold fmt expand unexpand tree stat sleep true ps pgrep top htop lsof pstree uptime w who last history bc expr md5 md5sum sha1sum sha256sum shasum base64 tput tty stty(read) getconf sysctl(no -w) defaults(read subcommand) sw_vers lscpu free vmstat iostat netstat ss ifconfig ip(read subcommands) route arp`.
- **Build/test tools treated as read** (they write to caches and `bin/`/`target/`/`dist/`, decision: that is the tool's own workspace, not a mutation the user cares to gate): `go build|vet|test|list|doc|version|env(no -w)|fmt(-l only, else fs:write)|mod graph|mod why|mod verify|tool`, `cargo build|check|test|clippy|doc|fmt(--check only, else fs:write)|tree|metadata`, `npm ls|view|outdated|audit(net)|test|run(Unknown — runs scripts)`, `tsc --noEmit`, `gofmt -l`(else fs:write), `golangci-lint`, `staticcheck`, `task --list`, `make -n`.
- **fs:write**: `cp mv mkdir touch chmod chown chgrp ln install dd truncate tee sed -i sed --in-place perl -i sort -o gzip gunzip bzip2 xz zip tar(x/c) unzip patch rsync mkfifo mknod sync(read) go fmt gofmt -w cargo fmt npm install(net) git clone(net) go install(net) go get(net) go mod tidy|download|vendor(net) go clean(fs:delete) go generate(Unknown) go run(Unknown)`.
- **fs:delete**: `rm rmdir unlink shred trash git clean(vcs) find -delete go clean`.
- **net**: `curl wget ssh scp sftp rsync(remote) nc ncat netcat telnet ping dig nslookup host traceroute mtr whois openssl s_client gh(all) aws(all, Unknown for mutating? — ponytail: all Unknown; too broad to table) git push|fetch|pull|clone|ls-remote|submodule|remote add|set-url npm install|ci|add|update|publish|link|view|audit pnpm/yarn same pip/pip3 install|download|uninstall(fs:write) go get|install|mod download|mod tidy cargo add|install|publish|update|fetch docker pull|push|login|build kubectl(all subcommands not in read set) brew install|upgrade|update|tap (also fs:write) apt apt-get dnf yum pacman(all: net|fs:write)`.
- **vcs**: `git commit|add|rm|mv|reset|checkout|switch|restore|rebase|merge|cherry-pick|revert|apply|am|stash(non-list)|tag(create)|branch(create/delete)|config(set)|clean(+fs:delete)|worktree add|remove|prune|notes|replace|filter-branch|update-ref|symbolic-ref|gc|prune|repack|init`, `git push|pull` (+net).
- **git read**: `status log diff show blame rev-parse rev-list ls-files ls-tree describe cat-file name-rev shortlog reflog grep count-objects fsck --version --help var check-ignore check-attr merge-base for-each-ref show-ref bisect(log|view only) diff-tree diff-index`.
- **Unknown (explicit, so intent is documented)**: `sudo -i`, `su`, `kill pkill killall`, `systemctl launchctl service`, `crontab`, `mount umount`, `chroot`, `nohup` with no inner, editors `vim vi nvim nano emacs code`, interpreters `python python3 node deno bun ruby perl php lua`, `make`(default target executes recipes), `task`(same), `npm run|exec|start`, `npx`, `yarn run`, `pnpm run`, `docker run|exec|compose`, `bash script.sh`, `source`, `.`, `aws`, `terraform`, `ansible*`, `open`, `xdg-open`, `osascript`.

Overrides from settings: `"classify": {"mytool": "read", "mytool deploy": "net", "sed -i": "fs:write"}` — keys use the same three forms; values are `Class.String()` syntax (`"read"`, `"fs:write,net"`). `"unknown"` in a value is a startup error (it would be a no-op anyway). Loaded in `cmd/app/settingsfile.go`, merged via `Table.Merge`, stored on `BashRules`.

### 4. `BashRules` changes (`internal/features/permissions/bash.go`)

```go
type BashRules struct {
    mu       sync.RWMutex
    allow    []string          // persisted allow globs (settings.json)
    session  []string          // in-memory allow globs — never persisted
    deny     []string
    catAllow shell.Class       // bitset from settings "categories.allow"
    catDeny  shell.Class       // bitset from settings "categories.deny"
    classify map[string]string // raw overrides from settings, kept for round-trip
    table    shell.Table       // shell.Builtin().Merge(parsed classify)
}
```

- `NewBashRules(allow, deny []string)` unchanged signature; table = `shell.Builtin()`.
- `UnmarshalJSON` reads the extended shape:
  ```json
  {"allow": [], "deny": [],
   "categories": {"allow": ["vcs"], "deny": ["fs:delete"]},
   "classify": {"mytool": "read"}}
  ```
  Unknown class names → error (startup error, consistent with malformed JSON being hard).
- `Lists() (allow, deny []string)` **returns persisted allow only** (this is what `BashAllowStore.Add` writes back). Add `SessionList() []string` for introspection/tests.
- `AllowPattern(p)` unchanged (persisted list). New `AllowPatternSession(p)` appends to `session`.
- `Categories() (allow, deny shell.Class)`, `Classify() map[string]string` for the settings writer.
- `Analyze(command string) shell.Analysis` — thin wrapper, `shell.Analyze(bashCommand(input), r.table)`; used by `Verdict`, `Suggest`, and (via `Policy.Bash`) nothing else.
- `Verdict(command string) (d core.Decision, reason string, ok bool)` — **signature change** (adds `reason`). Algorithm:
  1. `a := shell.Analyze(command, r.table)`.
  2. If `a.Err != nil`: if `matchAny(deny, command)` → `Deny, "bash command denied by permission policy", true`. Else → `AskUser, "could not parse command: <err>", true` (returning `ok=true` with AskUser is new — it lets the reason carry the parse error; `OnToolCall` only escalates, so it is safe).
  3. If `len(a.Segments) == 0` → `Allow, "", false` (assignment-only command; unchanged behaviour).
  4. Deny globs: for each segment, `matchAny(deny, seg.Raw) || matchAny(deny, seg.Text)` → `Deny, "bash command denied by permission policy: <seg.Text>", true`.
  5. Allow globs: mark segment covered if `matchAny(allow∪session, seg.Text)`.
  6. Category deny: for each **uncovered** segment, `seg.Class & catDeny != 0` → `Deny, "bash command denied by category <name>: <seg.Text>", true`.
  7. Category allow / read: for each uncovered segment, covered if `seg.Class & ^catAllow == 0` (i.e. every set bit is allowed; `Read` = 0 is trivially covered; `Unknown` bit can never be in `catAllow`).
  8. All covered → `Allow, "", true`. Else → `AskUser, a.Summary(), true`.
- `OnToolCall` in `permissions.go`: replace the `Verdict` call site to use the returned `reason` when non-empty; keep the existing "never lower" guard. A `Verdict` of `AskUser` with a summary reason sets `tcc.Reason` even when the name-level decision was already `AskUser` — the existing `if decision > tcc.Decision` guard would drop it, so set `tcc.Reason` when `decision == tcc.Decision && reason != ""` too.
- Delete: `splitExpressions`, `scanDoubleQuoted`, `matchParen`, `matchByte`, `stripEnvPrefix`, `cutAssignment`, `isNameStart`, `isNameByte`. Keep: `matchGlob`, `matchAny`, `bashCommand`.

### 5. `Suggest` changes (`suggest.go`)

- Uses `r.Analyze`. Iterates segments in order; skips segments covered by `allow∪session` **or** whose class is `Read` **or** covered by `catAllow` (these never prompt, so they never need a glob).
- First remaining segment: if its `Class.Has(shell.FSWrite)` came from a redirect or `tee` (check `Why` prefix `"redirect"` or `Argv[0].Value == "tee"`) → `"", "writes a file — approve case by case"` (unchanged behaviour). Otherwise `globFor(seg)`.
- `globFor(seg shell.Segment)`: operate on `seg.Argv` static values instead of `strings.Fields`. Wrapper-aware: if `Argv[0]` is a wrapper, glob is `"<wrapper> <inner-bin> *"` (`sudo rm *`, `xargs rm *`). `subcommandTools`/`flagSensitiveTools` logic unchanged otherwise. A non-static second word → fall back to `"<bin> *"`.
- Delete `writesFile`.
- `corpus_test.go` must still pass with the **same** expected globs and the same 15-rule convergence — with one caveat: since every corpus command is now read-only, `Suggest` returns `"", "already covered by the allow list"` for all of them under the new skip rule. Change the corpus test to call `globFor` on the first segment directly for the glob half, and add a third assertion: **every corpus command yields `Verdict → Allow, ok=true` with empty rules.** That is the headline test of this feature.

### 6. Session tier

- `api/types.go` `approveInput.Body`: add `Scope string json:"scope,omitempty" doc:"session|always (default always); with allow, session keeps the glob in memory only"`.
- `api/approvals.go` `handleApprove`: when `pattern != ""` and `Scope == "session"` → `s.cfg.BashAllow.Rules().AllowPatternSession(pattern)` instead of `persistAllow`; status `"allowed_session"`. Validation: unknown scope → 400.
- `internal/app/bashallow.go` `Add`: unchanged code, but now correct because `Lists()` excludes session globs. `writeBashSection` must round-trip `categories` and `classify`: change its signature to take the whole bash section (`allow, deny, categories, classify`) read from `Rules()` and write them all; other keys untouched as today.
- `cmd/app/connect.go` `connectApprove`: `ephemeral` path calls `AllowPatternSession` (not `AllowPattern`). Add `scope` to the protocol's `approve` command: `{id, call_id, approved, glob?, scope?}`; `scope: "session"` forces in-memory regardless of `ephemeral`; `scope: "always"` persists unless `ephemeral` is true (fleet default wins — document). `PROTOCOL.md` row updated; additive optional field, protocol version stays "1".
- UI (`api/ui/index.go`): beside `allow always` add `allow for session` button posting `{call_id, allow: pattern.value, scope: "session"}`. Render `d.data.reason` under the command for bash approvals (monospace, one line per segment — the `Summary()` format). `suggestGlob` prefill unchanged.

### 7. Bash tool

`internal/features/builtins/tool_bash.go`: `exec.CommandContext(tctx, "bash", "-c", command)`. Description → `"Execute a bash command in the project working directory."` Nothing else.

### 8. Read-only mode

`internal/harness/readonly.go`: in `OnToolCall`, before the deny, if `name == "bash"`:
```go
a := shell.Analyze(bashCommand(tcc.Call.Input), shell.Builtin())
if a.Err == nil && len(a.Segments) > 0 && a.Class().IsRead() { return nil }
// else fall through to Deny; Reason = "read-only mode: " + a.Summary()
```
`bashCommand` is currently unexported in `permissions`; either export it (`permissions.BashCommand`) or duplicate the 8-line JSON unmarshal in `readonly.go` — duplicate it (ponytail: no cross-package export for one helper). Note `len(a.Segments) == 0` (assignment-only) is denied here — nothing to run, nothing lost.

### 9. Settings loading (`cmd/app/settingsfile.go`)

No structural change: `settingsFile.Permissions map[string]*permissions.BashRules` still decodes via `UnmarshalJSON`, which now accepts the extra keys. `cmd/app/defaults/settings.json` gains empty `"categories": {"allow": [], "deny": []}` and `"classify": {}` so `tenzing init` shows the shape.

## Implementation steps

Each step compiles and passes tests on its own.

1. **Add dependency** — `go get mvdan.cc/sh/v3@v3.14.1 && go mod tidy`. → verify: `go build -o /dev/null ./...`.
2. **`shell` package: types + `Analyze` + redirects** (`shell.go`, `walk.go`, `shell_test.go`). Tests (table-driven, `{command, wantSegments []struct{text, class}}`):
   - `ls -la` → 1 segment read.
   - `FOO=1 go build ./... 2>&1 | head` → `go build ./...` read, `head` read; `Raw` of first includes `FOO=1`.
   - `echo hi > out.txt` → fs:write, Why contains `out.txt`. `echo hi > /dev/null`, `cmd 2>&1`, `cmd >&2` → read. `cmd >& log` → fs:write. `cmd &> log` → fs:write.
   - `echo "$(rm -rf x)"` → `echo` read at depth 0, `rm -rf x` fs:delete at depth 1.
   - `` echo `cat f` `` → both read, depth 1 for cat.
   - `diff <(ls a) <(ls b)` → 3 read segments.
   - `for f in *.go; do rm "$f"; done` → `rm "$f"` fs:delete with non-static Argv[1].
   - `{ echo a; echo b; } > out` → synthetic compound fs:write segment plus two read segments.
   - `f() { rm x; }; f` → `rm x` fs:delete, `f` unknown.
   - `[[ -f x ]] && echo yes` → 1 read segment (echo).
   - `cat <<EOF\n$(rm x)\nEOF` → cat read + rm fs:delete.
   - `ls 'unterminated` → `Err != nil`, `IsIncomplete`.
   - `$CMD --flag` → unknown "non-literal command word".
   - `X=1` alone → 0 segments.
3. **`shell` table** (`table.go`, `table_test.go`). Tests: every `Builtin()` key parses via the three key forms (no typos → a test that iterates the map and asserts each key has 1–3 space-separated tokens and the class is not Unknown-only where a read alternative exists); lookup cases: `sed -n 1p f` read, `sed -i 's/a/b/' f` fs:write, `sed -ni …` fs:write, `sed --in-place=.bak` fs:write, `git status` read, `git push` net|vcs, `git foo` unknown, `git -C dir log` read, `git branch` read, `git branch new` vcs, `git config --get x` read, `git config x y` vcs, `tar tzf a` read, `tar xzf a` fs:write, `tee /dev/null` read, `tee out` fs:write, `curl -o f url` net|fs:write, `awk '{print $1}'` read, `awk '{print > "f"}'` unknown, `/usr/bin/ls` read, `./ls` unknown, `ParseClass` round-trips `Class.String()`, `Merge` overrides win and does not mutate the receiver.
4. **`shell` wrappers** (`wrappers.go`, `wrappers_test.go`): `sudo rm -rf x` fs:delete Why has "via sudo"; `sudo -u bob ls` read; `sudo -i` unknown; `env FOO=1 go build` read; `xargs rm` fs:delete; `xargs` alone read; `find . -name '*.go' -exec rm {} \;` fs:delete; `find . -delete` fs:delete; `find . -exec cat {} + -exec rm {} \;` fs:delete (OR); `bash -c "rm -rf /tmp/x"` fs:delete depth 1; `bash -lc 'ls'` read; `bash -c "$X"` unknown; `bash script.sh` unknown; `eval "rm x"` fs:delete; `eval "$CMD"` unknown; `timeout 5 curl x` net; `nice -n 5 ls` read; `ssh host rm -rf /` net only (no recursion); nesting `bash -c 'bash -c "bash -c \"bash -c ls\""'` past cap → unknown.
5. **Rewire `BashRules`** (`bash.go`, `permissions.go`, `bash_test.go`, `permissions_test.go`). Delete the old splitter and its tests; port the existing `Verdict` tests to the new signature — every existing allow/deny expectation must still hold (`TOKEN=x go get` covered by `go *`; `LD_PRELOAD=*` deny hits the raw form; `a && b` with `b` denied → Deny; `ls > f` matched by `ls *`). New tests: read-only auto-allow (`grep x f | head` with empty rules → Allow ok); mixed (`ls; rm x` empty rules → AskUser, reason names `rm x`); category deny (`categories.deny: [fs:delete]`, `rm x` → Deny); category allow (`categories.allow: [vcs]`, `git commit -m x` → Allow; `git push` → AskUser because net not allowed); glob allow beats category deny (`allow: [rm *]`, `deny cat fs:delete`, `rm x` → Allow); parse error → AskUser with reason, and parse error + `deny: [rm *]` + `rm -rf / 'x` → Deny; session glob covers but `Lists()` excludes it; `UnmarshalJSON` rejects `"unknown"` and bad class names. → verify `go test -race ./internal/features/permissions/...`.
6. **`Suggest` + corpus** (`suggest.go`, `suggest_test.go`, `corpus_test.go`). Corpus gains the `Verdict → Allow` assertion for all 23. `Suggest` tests: wrapper glob `sudo rm *`; read segment skipped so `ls; rm x` suggests `rm *`; tee/redirect refusal unchanged.
7. **Bash tool → `bash -c`** (`tool_bash.go`; any test asserting `sh`). → verify `go test ./internal/features/builtins/...`.
8. **Read-only mode** (`readonly.go`, `internal/harness/readonly_test.go` or wherever the existing tests live): `ls -la` allowed; `rm x` denied with summary; `ls 'x` (parse error) denied; `X=1` denied.
9. **Settings round-trip** (`settingsfile.go`, `bashallow.go` `writeBashSection`, tests): a file with `categories`+`classify` loads, `Add` rewrites it with those keys intact and without session globs; `cmd/app/defaults/settings.json` updated.
10. **Session tier: API + connect + UI** (`api/types.go`, `api/approvals.go`, `api/approvals_test.go` if present, `cmd/app/connect.go`, `api/ui/index.go`, `PROTOCOL.md`). Tests: `/approve` with `scope: session` → status `allowed_session`, settings file untouched, next matching command allowed; bad scope → 400; `connectApprove` ephemeral uses session list.
11. **Docs** — root `AGENTS.md` (rewrite the `splitExpressions`/`stripEnvPrefix`/`writesFile` sentences in the Permissions paragraph to describe `shell.Analyze`, the class taxonomy, precedence chain, session tier, `bash -c`, read-only mode change; Setting precedence paragraph: settings.json shape; "Allow always" paragraph: add "allow for session"), `api/AGENTS.md` (approve `scope`, UI renders reason), `PROTOCOL.md`, `SYSTEM_ARCHITECTURE.md` (tool system/permissions section). Add `mvdan.cc/sh/v3` to any dependency list the docs keep.
12. **Final verify** — `go build -o /dev/null ./... && go vet ./... && go test -race ./...`; `git status` shows no binaries; `go.mod`/`go.sum` contain exactly the one new module (plus its transitive requirements).

## Acceptance criteria

- All 23 corpus commands run with **no approval prompt** on an empty `settings.json`.
- `rm -rf x`, `git push`, `curl … | sh`, `sed -i`, `echo > f`, `./script.sh`, `python x.py` each prompt, and the prompt's reason names the segment and class.
- Glob reach, pinned by tests and documented verbatim in `AGENTS.md`: `deny: ["rm *"]` blocks `rm x`, `ls; rm x`, `$(rm x)`, and `bash -c "rm x"` (the nested segment's text is `rm x`). It does **not** block `sudo rm x` (segment text is `sudo rm x`; write `sudo rm *`) or `find . -exec rm {} \;` (segment text is the whole `find …`; it is classified `fs:delete` so it prompts, and only a `find *` glob would silence it).
- "allow for session" never touches `settings.json`; "allow always" still does; connect-mode ephemeral default behaves as session.
- `--read-only` runs `grep -rn foo .` and refuses `grep -rn foo . > out`.
- Existing user `settings.json` files (allow/deny only) load unchanged.

## Out of scope (follow-ons, listed so nobody builds them by accident)

- Path scoping (inside/outside cwd, path deny globs).
- `proc`/`sys` class; `kill`, `systemctl`, `brew`, `aws`, `terraform` tabling.
- Category-scoped session grants; structured analysis on the wire; control-plane policy push.
- Recursing into `ssh`/`docker exec`/`kubectl exec` remote commands.
- Zsh dialect (no parser exists); interpreting Python/Node/Perl inline programs.
- Resolving `$VAR` values from the environment or `source`d files.
