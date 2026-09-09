package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// contextFilesMaxBytes caps the total context-file content appended to the
// system prompt; overflow is truncated with a marker. Sized for a real global
// config (CLAUDE.md + its imports + ~/.claude/rules) alongside a large
// project AGENTS.md, with room to grow — at 32KB a single sizeable AGENTS.md
// silently swallowed the whole budget.
const contextFilesMaxBytes = 128 * 1024

// maxImportDepth bounds @-import recursion, matching Claude Code.
const maxImportDepth = 5

// contextFileNames are the per-directory context files, in the order they are
// appended when a directory holds both.
var contextFileNames = []string{"AGENTS.md", "CLAUDE.md"}

// contextLoad is what loadContextFiles found: the text to append to the system
// prompt plus an account of where it came from. Rule files are tracked apart
// from context files because they are a distinct kind of input — standing
// policy rather than project context — and callers report them separately.
type contextLoad struct {
	content   string
	files     []string
	rules     []string
	truncated bool
}

// loadContextFiles collects context-file content for the system prompt: the
// globals <UserConfigDir>/tenzing/AGENTS.md, ~/.claude/CLAUDE.md and
// ~/.claude/rules/*.md first, then every AGENTS.md and CLAUDE.md on the
// ancestor chain from the filesystem root down to cwd (root→cwd order, so
// deeper files override by appearing later). A line that is just "@path" is
// replaced by the imported file's content. Content is empty when nothing
// exists; the path lists carry what was actually read, imports included, in
// the order appended, so callers can report what the agent was given.
func loadContextFiles(cwd string) contextLoad {
	var sb strings.Builder
	var out contextLoad
	seen := map[string]bool{}

	for _, c := range contextFilePaths(cwd) {
		body, imported, ok := readContextFile(c.path, seen, 0)
		if !ok {
			continue
		}
		read := append([]string{c.path}, imported...)
		if c.isRule {
			out.rules = append(out.rules, read...)
		} else {
			out.files = append(out.files, read...)
		}
		fmt.Fprintf(&sb, "\n\n# Context from %s\n\n%s", c.path, body)
	}

	content := sb.String()
	out.truncated = len(content) > contextFilesMaxBytes
	if out.truncated {
		content = content[:contextFilesMaxBytes] + fmt.Sprintf("\n\n[context files truncated at %dKB]", contextFilesMaxBytes/1024)
	}
	if content == "" {
		return contextLoad{}
	}
	out.content = "\n\n# Project context files (AGENTS.md / CLAUDE.md)" + content
	return out
}

// candidate is one path to try, tagged with whether it is a rule file so the
// caller can account for it separately.
type candidate struct {
	path   string
	isRule bool
}

// contextFilePaths returns every candidate path in load order: the global
// files, the rule files, then the ancestor chain root→cwd.
func contextFilePaths(cwd string) []candidate {
	var paths []candidate
	if base, err := os.UserConfigDir(); err == nil {
		paths = append(paths, candidate{path: filepath.Join(base, "tenzing", "AGENTS.md")})
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, candidate{path: filepath.Join(home, ".claude", "CLAUDE.md")})
		// Claude Code auto-discovers this directory rather than importing it,
		// so there is no @-import chain to follow — glob it directly. Glob
		// returns sorted matches, keeping the prompt byte-stable across runs.
		rules, _ := filepath.Glob(filepath.Join(home, ".claude", "rules", "*.md"))
		for _, r := range rules {
			paths = append(paths, candidate{path: r, isRule: true})
		}
	}
	for _, p := range ancestorChain(cwd) {
		paths = append(paths, candidate{path: p})
	}
	return paths
}

// readContextFile reads one context file and inlines its @-imports. It reports
// false for a file that is missing, empty, or already loaded — the seen set
// doubles as the import cycle guard and as de-duplication across candidates.
func readContextFile(path string, seen map[string]bool, depth int) (content string, imports []string, ok bool) {
	if seen[path] {
		return "", nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return "", nil, false
	}
	seen[path] = true

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i, line := range lines {
		target, isImport := importTarget(line, path)
		if !isImport || depth >= maxImportDepth {
			continue
		}
		body, nested, loaded := readContextFile(target, seen, depth+1)
		if !loaded {
			// Unresolvable or already-loaded import: leave the line as
			// written rather than silently blanking it.
			continue
		}
		lines[i] = fmt.Sprintf("# Imported from %s\n\n%s", target, body)
		imports = append(imports, target)
		imports = append(imports, nested...)
	}
	return strings.Join(lines, "\n"), imports, true
}

// importTarget resolves a line that is exactly "@path" into an absolute path,
// relative to the importing file's directory. "~/" is expanded against home.
func importTarget(line, from string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "@")
	if !ok || rest == "" || strings.ContainsAny(rest, " \t") {
		return "", false
	}
	if strings.HasPrefix(rest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, strings.TrimPrefix(rest, "~/")), true
	}
	if filepath.IsAbs(rest) {
		return filepath.Clean(rest), true
	}
	return filepath.Join(filepath.Dir(from), rest), true
}

// ancestorChain returns candidate context-file paths from the filesystem root
// down to cwd, inclusive, each directory contributing every contextFileNames
// entry it may hold.
func ancestorChain(cwd string) []string {
	var dirs []string
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == filepath.Dir(dir) {
			break
		}
	}
	// reverse: root first, cwd last
	paths := make([]string, 0, len(dirs)*len(contextFileNames))
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, name := range contextFileNames {
			paths = append(paths, filepath.Join(dirs[i], name))
		}
	}
	return paths
}
