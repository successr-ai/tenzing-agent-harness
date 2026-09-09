package permissions

import "strings"

// subcommandTools are binaries whose first non-flag word selects what the
// command actually does, so a useful glob keeps it: `git log *` rather than
// `git *`, which would also cover `git push`.
var subcommandTools = map[string]bool{
	"git": true, "go": true, "npm": true, "npx": true, "yarn": true,
	"pnpm": true, "cargo": true, "docker": true, "task": true, "gh": true,
	"kubectl": true, "brew": true, "mise": true, "aws": true, "terraform": true,
	"rtk": true,
}

// flagSensitiveTools are binaries where a leading flag, not a subcommand,
// decides whether the invocation reads or writes: `sed -n *` is a printer,
// `sed -i *` an editor.
//
// ponytail: sed is the only one that has come up. Add entries here if
// another read/write flag pair starts costing approvals.
var flagSensitiveTools = map[string]bool{"sed": true}

// Suggest proposes one allow glob for command: a rule covering the first
// expression the current allow list does not already match. Chained commands
// therefore converge one rule per approval instead of demanding the whole
// chain up front.
//
// glob is empty when no rule is proposed, and reason then says why — the
// command is already fully covered, or the expression writes a file and
// should be approved case by case rather than allowlisted (Verdict does not
// split on redirects, so a glob for the binary would cover the write too).
func (r *BashRules) Suggest(command string) (glob, reason string) {
	exprs := splitExpressions(command)

	r.mu.RLock()
	allow := r.allow
	r.mu.RUnlock()

	for _, e := range exprs {
		s := stripEnvPrefix(e)
		if s == "" || matchAny(allow, s) {
			continue
		}
		if writesFile(s) {
			return "", "writes a file — approve case by case"
		}
		return globFor(s), ""
	}
	return "", "already covered by the allow list"
}

// globFor builds the allow glob for a single expression: the binary, plus a
// subcommand or a distinguishing flag where one decides what the binary
// does, plus ' *'.
//
// ponytail: fields-based tokenizing. A quoted argument containing spaces in
// the subcommand slot (`git "log"`) would be mis-split; no real command does
// that, and the suggestion is editable anyway.
func globFor(expr string) string {
	f := strings.Fields(expr)
	if len(f) == 0 {
		return ""
	}
	bin := f[0]
	if len(f) > 1 {
		switch second := f[1]; {
		case subcommandTools[bin] && !strings.HasPrefix(second, "-"):
			return bin + " " + second + " *"
		case flagSensitiveTools[bin] && strings.HasPrefix(second, "-"):
			return bin + " " + second + " *"
		}
	}
	return bin + " *"
}

// writesFile reports whether an expression sends output to a file: a `>` or
// `>>` redirect outside quotes, or a tee. Descriptor duplications (`2>&1`,
// `>&2`) and discards (`2>/dev/null`) write nowhere new and do not count.
func writesFile(expr string) bool {
	// Expressions are already split on '|', so a tee is this expression's
	// own binary.
	if f := strings.Fields(expr); len(f) > 0 && f[0] == "tee" {
		return true
	}
	for i := 0; i < len(expr); {
		switch c := expr[i]; {
		case c == '\\' && i+1 < len(expr):
			i += 2
		case c == '\'' || c == '"':
			_, i = matchByte(expr, i+1, c)
		case c == '>':
			i++
			if i < len(expr) && expr[i] == '>' { // >>
				i++
			}
			for i < len(expr) && (expr[i] == ' ' || expr[i] == '\t') {
				i++
			}
			if i < len(expr) && expr[i] == '&' { // 2>&1, >&2
				continue
			}
			target := expr[i:]
			if j := strings.IndexAny(target, " \t"); j >= 0 {
				target = target[:j]
			}
			if target != "/dev/null" {
				return true
			}
		default:
			i++
		}
	}
	return false
}
