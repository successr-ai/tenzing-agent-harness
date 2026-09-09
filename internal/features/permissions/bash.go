package permissions

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// BashRules gates individual bash commands by glob, refining the name-level
// decision for the bash tool. The command is split into expressions and each
// is matched whole against the globs: any expression hitting Deny denies the
// whole command; every expression hitting Allow allows it; anything else
// leaves the name-level decision (normally AskUser) in place.
//
// Safe for concurrent use: the permission hook reads the lists while an
// approval driver may append to them (AllowPattern). The lists themselves
// are never mutated in place — appends copy — so a Verdict in flight keeps
// seeing a consistent snapshot.
type BashRules struct {
	mu    sync.RWMutex
	allow []string
	deny  []string
}

// NewBashRules builds rules from an allow and a deny list. The slices are
// copied; the caller keeps ownership of its own.
func NewBashRules(allow, deny []string) *BashRules {
	return &BashRules{
		allow: append([]string(nil), allow...),
		deny:  append([]string(nil), deny...),
	}
}

// bashRulesJSON is the settings-file shape of BashRules.
type bashRulesJSON struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

func (r *BashRules) UnmarshalJSON(b []byte) error {
	var j bashRulesJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allow, r.deny = j.Allow, j.Deny
	return nil
}

// Lists returns copies of the allow and deny patterns.
func (r *BashRules) Lists() (allow, deny []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.allow...), append([]string(nil), r.deny...)
}

// AllowPattern appends a glob to the allow list, taking effect for the rest
// of the session. Already-present patterns are a no-op.
func (r *BashRules) AllowPattern(pattern string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.allow, pattern) {
		return
	}
	r.allow = append(append([]string(nil), r.allow...), pattern)
}

// Verdict reports the decision for one bash command. ok is false when the
// rules have nothing to say and the caller should keep its own decision.
//
// Deny runs first over every expression, then allow: a conflict — an
// expression matching both lists, or one denied expression in an otherwise
// allowed chain — always resolves to Deny.
func (r *BashRules) Verdict(command string) (d core.Decision, ok bool) {
	exprs := splitExpressions(command)
	if len(exprs) == 0 {
		return core.Allow, false
	}

	// Leading NAME=value assignments are stripped before matching, so
	// `TOKEN=x go get ./...` is covered by a `go *` glob.
	stripped := make([]string, len(exprs))
	for i, e := range exprs {
		stripped[i] = stripEnvPrefix(e)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Deny pass. Matched against BOTH the raw and the stripped form, so an
	// env prefix cannot bypass a deny rule and a rule can still target the
	// assignment itself ("LD_PRELOAD=*").
	for i, raw := range exprs {
		if matchAny(r.deny, raw) || matchAny(r.deny, stripped[i]) {
			return core.Deny, true
		}
	}

	// Allow pass. Every expression must be covered; one that is nothing but
	// assignments executes no command, so it neither needs covering nor
	// blocks the others.
	for _, e := range stripped {
		if e != "" && !matchAny(r.allow, e) {
			return core.Allow, false
		}
	}
	return core.Allow, true
}

func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if matchGlob(p, s) {
			return true
		}
	}
	return false
}

// bashCommand pulls the command out of a bash tool call's JSON input.
// Unparseable input yields "", which splits to no expressions and so leaves
// the name-level decision alone.
func bashCommand(input string) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ""
	}
	return args.Command
}

// splitExpressions chops a shell command into the expressions a policy glob
// is matched against. It splits on &&, ||, ;, |, & and newline, skipping
// separators inside single and double quotes, and additionally yields the
// bodies of $(…) and `…` as expressions of their own (the substitution text
// also stays in its enclosing expression). Redirects are deliberately NOT
// separators: `ls > f` is one expression, so a glob like "ls *" covers it.
//
// The returned order is unspecified — callers only ask whether every/any
// expression matches.
func splitExpressions(s string) []string {
	var out []string
	var buf strings.Builder
	flush := func() {
		if e := strings.TrimSpace(buf.String()); e != "" {
			out = append(out, e)
		}
		buf.Reset()
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			buf.WriteString(s[i : i+2])
			i += 2
		case c == '\'':
			_, next := matchByte(s, i+1, '\'')
			buf.WriteString(s[i:next])
			i = next
		case c == '"':
			end, subs := scanDoubleQuoted(s, i)
			buf.WriteString(s[i:end])
			out = append(out, subs...)
			i = end
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			body, next := matchParen(s, i+1)
			out = append(out, splitExpressions(s[i+2:body])...)
			buf.WriteString(s[i:next])
			i = next
		case c == '`':
			body, next := matchByte(s, i+1, '`')
			out = append(out, splitExpressions(s[i+1:body])...)
			buf.WriteString(s[i:next])
			i = next
		case (c == '&' || c == '|') && i+1 < len(s) && s[i+1] == c:
			flush()
			i += 2
		case c == ';' || c == '|' || c == '\n':
			flush()
			i++
		// A bare & backgrounds the command before it, so it separates two
		// expressions. Redirect forms that merely contain one (2>&1, &>file)
		// do not.
		case c == '&' && !(i > 0 && s[i-1] == '>') && !(i+1 < len(s) && s[i+1] == '>'):
			flush()
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// scanDoubleQuoted returns the index just past the '"' closing the one at
// start, plus any expressions from command substitutions inside it (which
// shells still evaluate within double quotes). An unterminated quote runs to
// the end of the string.
func scanDoubleQuoted(s string, start int) (int, []string) {
	var subs []string
	for i := start + 1; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i += 2
		case s[i] == '"':
			return i + 1, subs
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
			body, next := matchParen(s, i+1)
			subs = append(subs, splitExpressions(s[i+2:body])...)
			i = next
		case s[i] == '`':
			body, next := matchByte(s, i+1, '`')
			subs = append(subs, splitExpressions(s[i+1:body])...)
			i = next
		default:
			i++
		}
	}
	return len(s), subs
}

// matchParen finds the ')' closing the '(' at open, allowing nesting.
// body is the index of that ')' and next the index just past it; both are
// len(s) when the parenthesis is never closed.
func matchParen(s string, open int) (body, next int) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i, i + 1
			}
		}
	}
	return len(s), len(s)
}

// matchByte finds the closing delimiter c at or after from. body is its
// index and next the index just past it; both are len(s) when c never
// appears again.
func matchByte(s string, from int, c byte) (body, next int) {
	if j := strings.IndexByte(s[from:], c); j >= 0 {
		return from + j, from + j + 1
	}
	return len(s), len(s)
}

// stripEnvPrefix removes leading NAME=value assignments from an expression.
// Returns "" when the expression is nothing but assignments. Command
// substitutions inside an assignment's value are already separate
// expressions (splitExpressions extracts them), so dropping the text here
// cannot hide a denied command.
func stripEnvPrefix(expr string) string {
	for {
		rest, ok := cutAssignment(expr)
		if !ok {
			return expr
		}
		expr = rest
	}
}

// cutAssignment removes one leading NAME=value token and the whitespace
// after it, reporting whether the expression started with an assignment.
// The value runs to the first unquoted space.
func cutAssignment(expr string) (string, bool) {
	if len(expr) == 0 || !isNameStart(expr[0]) {
		return expr, false
	}
	i := 1
	for i < len(expr) && isNameByte(expr[i]) {
		i++
	}
	if i >= len(expr) || expr[i] != '=' {
		return expr, false
	}
	for i++; i < len(expr); {
		switch c := expr[i]; {
		case c == '\\' && i+1 < len(expr):
			i += 2
		case c == '\'' || c == '"':
			_, i = matchByte(expr, i+1, c)
		case c == ' ' || c == '\t':
			for i < len(expr) && (expr[i] == ' ' || expr[i] == '\t') {
				i++
			}
			return expr[i:], true
		default:
			i++
		}
	}
	return "", true
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameByte(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// matchGlob reports whether pattern matches the whole of s. '*' matches any
// run of bytes (including '/', unlike path.Match — commands are not paths),
// '?' matches exactly one byte, and every other byte is literal.
func matchGlob(pattern, s string) bool {
	var pi, si, star, mark int
	star = -1
	for si < len(s) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]):
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
