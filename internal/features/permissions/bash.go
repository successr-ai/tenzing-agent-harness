package permissions

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
)

// BashRules gates individual bash commands, refining the name-level
// decision for the bash tool. The command is parsed (shell.Analyze) into
// the simple commands it runs, each classified for what it mutates, and the
// decision falls out of, in order: deny globs, allow globs (persisted or
// session), category deny, category allow plus read-only auto-allow. Any
// segment hitting a deny denies the whole command; Allow needs every
// segment covered; anything else leaves the name-level decision (normally
// AskUser) in place, with the analysis summary as the reason.
//
// Safe for concurrent use: the permission hook reads while an approval
// driver appends (AllowPattern / AllowPatternSession). The lists are
// copy-on-write, so a Verdict in flight keeps a consistent snapshot.
type BashRules struct {
	mu       sync.RWMutex
	allow    []string // persisted allow globs (settings.json)
	session  []string // in-memory allow globs, never written back
	deny     []string
	catAllow shell.Class
	catDeny  shell.Class
	classify map[string]string // raw settings overrides, kept for round-trip
	table    shell.Table
}

// NewBashRules builds rules from an allow and a deny list with the built-in
// knowledge table. The slices are copied; the caller keeps ownership.
func NewBashRules(allow, deny []string) *BashRules {
	return &BashRules{
		allow: append([]string(nil), allow...),
		deny:  append([]string(nil), deny...),
		table: shell.Builtin(),
	}
}

// bashRulesJSON is the settings-file shape of BashRules.
type bashRulesJSON struct {
	Allow      []string `json:"allow"`
	Deny       []string `json:"deny"`
	Categories *struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"categories,omitempty"`
	Classify map[string]string `json:"classify,omitempty"`
}

// UnmarshalJSON decodes the settings-file shape. Unknown class names — and
// "unknown" itself — are errors: a security policy that does not parse must
// not be silently narrowed.
func (r *BashRules) UnmarshalJSON(b []byte) error {
	var j bashRulesJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	var catAllow, catDeny shell.Class
	if j.Categories != nil {
		var err error
		if catAllow, err = parseClasses(j.Categories.Allow); err != nil {
			return fmt.Errorf("categories.allow: %w", err)
		}
		if catDeny, err = parseClasses(j.Categories.Deny); err != nil {
			return fmt.Errorf("categories.deny: %w", err)
		}
	}
	overrides := make(map[string]shell.Class, len(j.Classify))
	for k, v := range j.Classify {
		c, err := shell.ParseClass(v)
		if err != nil {
			return fmt.Errorf("classify %q: %w", k, err)
		}
		overrides[strings.Join(strings.Fields(k), " ")] = c
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.allow, r.deny = j.Allow, j.Deny
	r.session = nil
	r.catAllow, r.catDeny = catAllow, catDeny
	r.classify = j.Classify
	r.table = shell.Builtin().Merge(overrides)
	return nil
}

func parseClasses(names []string) (shell.Class, error) {
	var c shell.Class
	for _, n := range names {
		p, err := shell.ParseClass(n)
		if err != nil {
			return 0, err
		}
		c |= p
	}
	return c, nil
}

// Lists returns copies of the persisted allow and deny patterns. Session
// grants are deliberately excluded: this is what gets written back to the
// settings file.
func (r *BashRules) Lists() (allow, deny []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.allow...), append([]string(nil), r.deny...)
}

// SessionList returns a copy of the in-memory allow patterns.
func (r *BashRules) SessionList() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.session...)
}

// Categories returns the class sets allowed and denied by category rules.
func (r *BashRules) Categories() (allow, deny shell.Class) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catAllow, r.catDeny
}

// Classify returns a copy of the raw classification overrides.
func (r *BashRules) Classify() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.classify))
	for k, v := range r.classify {
		out[k] = v
	}
	return out
}

// AllowPattern appends a glob to the persisted allow list, taking effect
// for the rest of the session. Already-present patterns are a no-op.
func (r *BashRules) AllowPattern(pattern string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.allow, pattern) {
		return
	}
	r.allow = append(append([]string(nil), r.allow...), pattern)
}

// AllowPatternSession appends a glob to the session-only allow list. It is
// never persisted; already-present patterns are a no-op.
func (r *BashRules) AllowPatternSession(pattern string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.session, pattern) || slices.Contains(r.allow, pattern) {
		return
	}
	r.session = append(append([]string(nil), r.session...), pattern)
}

// Analyze parses and classifies command with the rules' knowledge table.
func (r *BashRules) Analyze(command string) shell.Analysis {
	r.mu.RLock()
	t := r.table
	r.mu.RUnlock()
	return shell.Analyze(command, t)
}

// snapshot is one consistent view of the rules for a single decision.
type snapshot struct {
	allow, deny       []string
	catAllow, catDeny shell.Class
}

func (r *BashRules) snapshot() snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	allow := make([]string, 0, len(r.allow)+len(r.session))
	allow = append(allow, r.allow...)
	allow = append(allow, r.session...)
	return snapshot{allow: allow, deny: r.deny, catAllow: r.catAllow, catDeny: r.catDeny}
}

// globAllowed reports whether an allow glob covers the segment.
func (s snapshot) globAllowed(seg shell.Segment) bool {
	_, ok := matchingGlob(s.allow, seg.Text)
	return ok
}

// covered reports whether the segment needs no approval: a class whose
// every bit is category-allowed (read-only trivially is; Unknown never can
// be), or an allow glob — with one carve-out. A glob covers the binary it
// names, not a redirect bolted onto it: `cat *` must not allowlist `cat >
// anything`. A redirect write is covered only when the glob itself spells
// the redirect (`cat >*`) or fs:write is category-allowed.
func (s snapshot) covered(seg shell.Segment) bool {
	if seg.Class&^s.catAllow == 0 {
		return true
	}
	glob, ok := matchingGlob(s.allow, seg.Text)
	if !ok {
		return false
	}
	if writesViaRedirect(seg) && !strings.Contains(glob, ">") && !s.catAllow.Has(shell.FSWrite) {
		return false
	}
	return true
}

// Verdict reports the decision for one bash command. ok is false when the
// rules have nothing to say (a command that runs nothing) and the caller
// should keep its own decision. reason is set for Deny and for an AskUser
// that carries the analysis summary; it is empty on Allow.
func (r *BashRules) Verdict(command string) (d core.Decision, reason string, ok bool) {
	a := r.Analyze(command)
	s := r.snapshot()

	if a.Err != nil {
		// No trustworthy split: deny globs get the whole raw string as a
		// last line, otherwise a human decides with the error in view.
		if matchAny(s.deny, command) {
			return core.Deny, "bash command denied by permission policy", true
		}
		return core.AskUser, a.Summary(), true
	}
	if len(a.Segments) == 0 {
		return core.Allow, "", false
	}

	// Deny globs, raw and stripped, so an env prefix cannot bypass a rule
	// and a rule can still target the assignment itself ("LD_PRELOAD=*").
	for _, seg := range a.Segments {
		if matchAny(s.deny, seg.Raw) || matchAny(s.deny, seg.Text) {
			return core.Deny, "bash command denied by permission policy: " + seg.Text, true
		}
	}
	// Category deny applies to segments no allow glob has claimed.
	for _, seg := range a.Segments {
		if !s.globAllowed(seg) && seg.Class&s.catDeny != 0 {
			return core.Deny, fmt.Sprintf("bash command denied by category %s: %s", (seg.Class & s.catDeny), seg.Text), true
		}
	}
	for _, seg := range a.Segments {
		if !s.covered(seg) {
			return core.AskUser, a.Summary(), true
		}
	}
	return core.Allow, "", true
}

func matchAny(patterns []string, s string) bool {
	_, ok := matchingGlob(patterns, s)
	return ok
}

// matchingGlob returns the first pattern matching s.
func matchingGlob(patterns []string, s string) (string, bool) {
	for _, p := range patterns {
		if matchGlob(p, s) {
			return p, true
		}
		// A trailing " *" reads as "with any arguments", and no arguments
		// is a case of that: `head *` covers a bare `head` in `grep x f |
		// head`. Without this a rule can never cover the argument-less form,
		// and on the deny side it would leave `git commit` open to a rule
		// written as `git commit *`.
		if strings.HasSuffix(p, " *") && s == strings.TrimSuffix(p, " *") {
			return p, true
		}
	}
	return "", false
}

// bashCommand pulls the command out of a bash tool call's JSON input.
// Unparseable input yields "", which analyses to no segments and so leaves
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
