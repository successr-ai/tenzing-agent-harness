package permissions

import (
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
)

// subcommandTools are binaries whose first non-flag word selects what the
// command actually does, so a useful glob keeps it: `git log *` rather than
// `git *`, which would also cover `git push`.
var subcommandTools = map[string]bool{
	"git": true, "go": true, "npm": true, "npx": true, "yarn": true,
	"pnpm": true, "cargo": true, "docker": true, "task": true, "gh": true,
	"kubectl": true, "brew": true, "mise": true, "aws": true, "terraform": true,
	"rtk": true,
	// Wrappers: the glob keeps the inner binary (`sudo rm *`), so a rule
	// for the wrapper never silently covers everything it can run.
	"sudo": true, "env": true, "xargs": true, "timeout": true, "nice": true,
	"nohup": true, "time": true, "command": true, "exec": true,
}

// flagSensitiveTools are binaries where a leading flag, not a subcommand,
// decides whether the invocation reads or writes: `sed -n *` is a printer,
// `sed -i *` an editor.
//
// ponytail: sed is the only one that has come up. Add entries here if
// another read/write flag pair starts costing approvals.
var flagSensitiveTools = map[string]bool{"sed": true}

// Suggest proposes one allow glob for command: a rule covering the first
// segment that would still prompt — not covered by an allow glob, not
// read-only, not category-allowed. Chained commands therefore converge one
// rule per approval instead of demanding the whole chain up front.
//
// glob is empty when no rule is proposed, and reason then says why — the
// command would not prompt at all, or the segment writes a file through a
// redirect or tee and should be approved case by case rather than
// allowlisted (a glob for the binary would cover the write too).
func (r *BashRules) Suggest(command string) (glob, reason string) {
	a := r.Analyze(command)
	if a.Err != nil {
		return "", "could not parse command — approve case by case"
	}
	s := r.snapshot()
	for _, seg := range a.Segments {
		if s.covered(seg) {
			continue
		}
		if writesViaRedirect(seg) || isTee(seg) {
			return "", "writes a file — approve case by case"
		}
		if g := globFor(seg); g != "" {
			return g, ""
		}
		return "", "command is not statically analysable — approve case by case"
	}
	return "", "already covered by the allow list"
}

// writesViaRedirect reports whether the segment's write comes from a
// redirect rather than from the binary itself. Verdict uses it too: a glob
// for the binary does not cover a redirect.
func writesViaRedirect(seg shell.Segment) bool {
	for _, w := range seg.Why {
		if strings.HasPrefix(w, "redirect ") {
			return true
		}
	}
	return false
}

// isTee: tee writes by definition, and a `tee *` glob would allowlist
// writing anywhere, so Suggest never proposes one.
func isTee(seg shell.Segment) bool {
	return len(seg.Argv) > 0 && seg.Argv[0].Static && seg.Argv[0].Value == "tee" && seg.Class.Has(shell.FSWrite)
}

// globFor builds the allow glob for a single segment: the binary, plus a
// subcommand or a distinguishing flag where one decides what the binary
// does, plus ' *'. Non-static words fall back to the binary alone.
func globFor(seg shell.Segment) string {
	argv := seg.Argv
	if len(argv) == 0 || !argv[0].Static {
		// A compound-command redirect or a $CMD: nothing to name.
		return ""
	}
	bin := argv[0].Value
	if len(argv) > 1 && argv[1].Static {
		switch second := argv[1].Value; {
		case subcommandTools[bin] && !strings.HasPrefix(second, "-"):
			return bin + " " + second + " *"
		case flagSensitiveTools[bin] && strings.HasPrefix(second, "-"):
			return bin + " " + second + " *"
		}
	}
	return bin + " *"
}
