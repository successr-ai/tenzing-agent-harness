// Package shell parses a bash command line with mvdan.cc/sh and breaks it
// into the simple commands it runs, classifying each for the resources it
// mutates. It is pure: no core import, no I/O, no environment — a command's
// classification depends only on its text and the knowledge Table.
package shell

import (
	"fmt"
	"strings"
)

// Class is a bitset of resource kinds a command touches. Read is the empty
// set. Unknown marks a command the table has no verdict on; it is never
// auto-allowed and cannot be named in a category rule.
type Class uint8

// Read has no bits set: the command mutates nothing this package tracks.
const Read Class = 0

const (
	FSWrite  Class = 1 << iota // creates or modifies files
	FSDelete                   // removes files
	Net                        // outbound network traffic or remote execution
	VCS                        // local repository state
	Unknown                    // not in the table, or not statically analysable
)

var classNames = []struct {
	c    Class
	name string
}{
	{FSWrite, "fs:write"},
	{FSDelete, "fs:delete"},
	{Net, "net"},
	{VCS, "vcs"},
	{Unknown, "unknown"},
}

// IsRead reports whether the class carries no mutation bits.
func (c Class) IsRead() bool { return c == Read }

// Has reports whether every bit of o is set in c.
func (c Class) Has(o Class) bool { return c&o == o }

// String renders the set as sorted, comma-joined names; "read" when empty.
func (c Class) String() string {
	if c.IsRead() {
		return "read"
	}
	var names []string
	for _, cn := range classNames {
		if c.Has(cn.c) {
			names = append(names, cn.name)
		}
	}
	return strings.Join(names, ",")
}

// ParseClass is the inverse of String, for settings files. "unknown" is
// rejected: a rule naming it could never allow anything and would only
// confuse.
func ParseClass(s string) (Class, error) {
	if strings.TrimSpace(s) == "read" {
		return Read, nil
	}
	var c Class
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		found := false
		for _, cn := range classNames {
			if cn.name == part && cn.c != Unknown {
				c |= cn.c
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("unknown class %q (want read, fs:write, fs:delete, net, vcs)", part)
		}
	}
	return c, nil
}

// Arg is one word of a simple command after static evaluation. Static is
// false when the word depends on runtime state ($VAR, $(cmd), arithmetic) —
// Value is then "". Glob characters stay literal: `*.pem` is static.
type Arg struct {
	Value  string
	Static bool
	// Template is set for a non-static word whose only expansions are plain
	// parameters: `$HOME/.ssh` reads "${HOME}/.ssh", ready for os.Expand. A
	// word with any other expansion ($(…), ${a:-b}, $((…))) has none.
	Template string
}

// Segment is one simple command found in the command line.
type Segment struct {
	// Text is the command as printed by mvdan's printer WITHOUT leading
	// NAME=value assignments — allow and deny globs match this.
	Text string
	// Raw is the same WITH assignments — deny globs additionally match it,
	// so a rule like "LD_PRELOAD=*" can target the assignment itself.
	Raw  string
	Argv []Arg
	// Redirects are the file targets of the command's redirects, input and
	// output alike (`> out`, `< in`, `&>> log`), evaluated like Argv.
	// Descriptor duplications, /dev sinks and here-doc delimiters are left
	// out.
	Redirects []Arg
	Class     Class
	// Why explains the classification, one reason per contribution.
	Why []string
	// Depth is 0 at top level and grows by one for each $(…), `…`, <(…),
	// bash -c or eval the command sits inside.
	Depth int
}

// Analysis is the result of Analyze. Err is a parse error; Segments is
// empty when it is set.
type Analysis struct {
	Segments []Segment
	Err      error
}

// Class is the union of every segment's class.
func (a Analysis) Class() Class {
	var c Class
	for _, s := range a.Segments {
		c |= s.Class
	}
	return c
}

// Summary is the human-readable reason shown on an approval prompt: the
// parse error, "read-only", or one line per mutating segment.
func (a Analysis) Summary() string {
	if a.Err != nil {
		return "parse error: " + a.Err.Error()
	}
	var lines []string
	for _, s := range a.Segments {
		if s.Class.IsRead() {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-10s %s  (%s)", s.Class, s.Text, strings.Join(s.Why, "; ")))
	}
	if len(lines) == 0 {
		return "read-only"
	}
	return strings.Join(lines, "\n")
}
