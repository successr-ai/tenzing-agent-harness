package systemone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// maxPathFacts bounds how many locations one call reports. A command that
// touches more than this is described by its first few operands; the state
// stays small, which is what keeps the judgment attributable.
const maxPathFacts = 8

// pathArgs are the tool arguments that name a filesystem location. Native
// tools declare their paths in the schema, so these are exact rather than
// guessed; bash is handled separately, through the shell parser.
var pathArgs = []string{"file_path", "path", "dir", "notebook_path"}

// pathFact is one location a tool call touches, resolved and placed. The
// questions ask about intent; where a call lands is not a matter of opinion,
// so it is computed here and handed to the model as fact. Without it a
// decision model reads `rm -r backup` as a string and cannot tell whether
// `backup` is a scratch directory or someone's only copy.
type pathFact struct {
	Path                    string `json:"path"`
	OutsideWorkingDirectory bool   `json:"outside_working_directory"`
	InTempDirectory         bool   `json:"in_temp_directory"`
}

// pathsFor resolves and classifies every location a call names. Best-effort
// by nature: it reports what it found, never that it found everything — a
// command building a path at runtime ($VAR, $(…), a glob) cannot be resolved
// before it runs, and those words are skipped rather than guessed at.
func (e *Ext) pathsFor(call core.ToolCall) []pathFact {
	var raw []string
	var input map[string]any
	if err := json.Unmarshal([]byte(call.Input), &input); err == nil {
		for _, key := range pathArgs {
			if s, ok := input[key].(string); ok && s != "" {
				raw = append(raw, s)
			}
		}
		// Glob takes no path argument: its pattern is the location, and a
		// pattern's literal prefix resolves like any other path. Grep's
		// pattern is a regex, which is why this is by tool.
		if pat, ok := input["pattern"].(string); ok && pat != "" && strings.EqualFold(call.Name, "glob") {
			raw = append(raw, pat)
		}
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			raw = append(raw, e.commandPaths(cmd)...)
		}
	}

	seen := make(map[string]bool, len(raw))
	facts := make([]pathFact, 0, len(raw))
	for _, p := range raw {
		f := e.classifyPath(p)
		if f.Path == "" || seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		facts = append(facts, f)
		if len(facts) == maxPathFacts {
			break
		}
	}
	if len(facts) == 0 {
		return nil
	}
	return facts
}

// commandPaths pulls the path-shaped operands out of a shell command line.
// The split comes from the late-bound shell parser (the same one the
// permission rules use), so quoting, chains and substitutions are handled by
// a real grammar rather than by splitting on spaces. Unbound, no paths are
// reported for bash — the fact is absent, never wrong.
func (e *Ext) commandPaths(command string) []string {
	e.mu.Lock()
	split := e.shellArgs
	e.mu.Unlock()
	if split == nil {
		return nil
	}
	var out []string
	words := split(command)
	for i, word := range words {
		if i > 0 && codeFlags[words[i-1]] && isCode(word) {
			out = append(out, embeddedPaths(word)...)
			continue
		}
		if word = flagValue(word); looksLikePath(word, e.workdir) {
			out = append(out, word)
		}
	}
	return out
}

// codeFlags introduce an inline program: `python3 -c`, `node -e`,
// `perl -E`, `ruby -e`, `deno eval`'s `--eval`. Only the word after one is
// scanned for embedded paths — a commit message or a grep pattern that
// mentions "/etc" is prose, not a location.
var codeFlags = map[string]bool{"-c": true, "-e": true, "-E": true, "--eval": true, "--command": true}

// isCode reports whether the word after a code flag is a program rather
// than a one-word operand. Treating `open('/etc/passwd')` as one path would
// place it inside the working directory.
func isCode(word string) bool { return strings.ContainsAny(word, " \t\n'\"()") }

// embeddedPath matches a path written as a literal inside code: absolute,
// home-relative or climbing out with `../`, opened by a quote, bracket,
// space or `=`. A `:` does not open one, so `https://host/x` stays a URL.
var embeddedPath = regexp.MustCompile(`(?:^|[\s'"(=,\[{])((?:~|\.\.)?/[^\s'"(),;\]}]+)`)

// embeddedPaths pulls the path literals out of a piece of code.
// ponytail: literal-only scan — a path assembled at runtime
// (os.path.join, string concatenation) is not seen; a sandbox is the
// upgrade if interpreters must be contained.
func embeddedPaths(code string) []string {
	var out []string
	for _, m := range embeddedPath.FindAllStringSubmatch(code, -1) {
		out = append(out, m[1])
	}
	return out
}

// flagValue unwraps a long flag's attached value — `--target-directory=/x`
// names /x — and leaves every other word as written. A bare flag comes back
// unchanged, and looksLikePath skips it.
func flagValue(word string) string {
	if !strings.HasPrefix(word, "--") {
		return word
	}
	if _, v, ok := strings.Cut(word, "="); ok {
		return v
	}
	return word
}

// looksLikePath keeps the operands worth resolving: anything written as a
// path, and anything naming something that is actually there. Flags are
// skipped, and so is a bare word that matches nothing — `rm backup` is about
// a directory, `git status` is not about a file called "status".
func looksLikePath(word, workdir string) bool {
	switch {
	case word == "", strings.HasPrefix(word, "-"):
		return false
	case strings.ContainsRune(word, filepath.Separator), strings.HasPrefix(word, "~"):
		return true
	}
	_, err := os.Lstat(filepath.Join(workdir, word))
	return err == nil
}

// classifyPath resolves one path against the working directory and places it.
// Relative paths resolve against the working directory (the agent's own
// frame), "~" against the home directory, and symlinks are followed as far as
// the path exists — on macOS /tmp is a symlink to /private/tmp, so comparing
// unresolved paths would report the system temp directory as somewhere else.
//
// The working directory wins: a path inside it is the user's project wherever
// the project happens to sit, so it is never "temp" even when the project
// lives under the system temp root. Measured live: without this rule, a
// workspace under /private/tmp made deleting its untracked directory read as
// scratch cleanup (irreversible 0.47, no prompt) rather than as a deletion.
func (e *Ext) classifyPath(p string) pathFact {
	abs := resolve(p, e.workdir)
	if abs == "" {
		return pathFact{}
	}
	inside := within(e.workdir, abs)
	return pathFact{
		Path:                    abs,
		OutsideWorkingDirectory: !inside,
		InTempDirectory:         !inside && e.inTemp(abs),
	}
}

// resolve turns a path as written into an absolute, symlink-resolved one.
// A path that does not exist yet still resolves: the existing part of it is
// followed and the rest appended, so a file about to be created is placed as
// accurately as one already there.
func resolve(p, workdir string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workdir, p)
	}
	return followSymlinks(filepath.Clean(p))
}

// followSymlinks resolves the longest existing prefix of p and re-appends
// the rest, so a not-yet-created file under a symlinked directory still
// lands in the right place.
func followSymlinks(p string) string {
	rest := ""
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur { // reached the root without finding anything
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// tempRoots are the scratch locations an agent may use freely: the
// process's temp directory and /tmp. On macOS they differ — os.TempDir() is
// a per-user /var/folders path, while /tmp is where commands and people
// actually put scratch files — so both count. Resolved, since /tmp is itself
// a symlink there.
func tempRoots() []string {
	roots := []string{followSymlinks(os.TempDir())}
	if tmp := followSymlinks("/tmp"); tmp != roots[0] {
		roots = append(roots, tmp)
	}
	return roots
}

func (e *Ext) inTemp(path string) bool {
	for _, root := range e.tempdirs {
		if within(root, path) {
			return true
		}
	}
	return false
}

// within reports whether path sits inside root. A path on another volume, or
// one Rel cannot express, counts as outside — the safe reading.
func within(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}
