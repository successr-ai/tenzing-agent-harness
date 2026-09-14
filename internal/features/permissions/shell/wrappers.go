package shell

import (
	"fmt"
	"path"
	"strings"
)

// binDirs are the only directories an absolute command path is resolved
// through: /usr/bin/ls is ls, but ./ls and /opt/x/ls are not.
//
// ponytail: fixed list; extend when a real command needs it.
var binDirs = map[string]bool{
	"/bin": true, "/usr/bin": true, "/usr/local/bin": true, "/opt/homebrew/bin": true, "/sbin": true, "/usr/sbin": true,
}

// resolveBin maps a static command word to a table key, or "" when the
// path cannot be trusted to name the binary it looks like.
func resolveBin(word string) string {
	if !strings.Contains(word, "/") {
		return word
	}
	if binDirs[path.Dir(word)] {
		return path.Base(word)
	}
	return ""
}

// stringShells run a script given with -c.
var stringShells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

// wrapper describes a command that runs another: skip is the number of
// value arguments each flag consumes (flags not listed take none); empty
// is the class when nothing follows the flags.
type wrapper struct {
	valueFlags map[string]bool
	empty      Class
	emptyWhy   string
}

var wrappers = map[string]wrapper{
	"sudo":       {valueFlags: set("-u", "-g", "-C", "-h", "-p", "-U", "-r", "-t"), empty: Unknown, emptyWhy: "sudo without a command opens a shell"},
	"doas":       {valueFlags: set("-u", "-C"), empty: Unknown, emptyWhy: "doas without a command opens a shell"},
	"env":        {valueFlags: set("-u", "-C", "-S", "--unset", "--chdir"), empty: Read},
	"command":    {empty: Read},
	"builtin":    {empty: Read},
	"exec":       {empty: Read},
	"nohup":      {empty: Unknown, emptyWhy: "nohup without a command"},
	"time":       {valueFlags: set("-f", "-o"), empty: Read},
	"caffeinate": {valueFlags: set("-t", "-w"), empty: Read},
	"nice":       {valueFlags: set("-n", "--adjustment"), empty: Read},
	"ionice":     {valueFlags: set("-c", "-n", "-p"), empty: Read},
	"stdbuf":     {valueFlags: set("-i", "-o", "-e"), empty: Read},
	"xargs":      {valueFlags: set("-n", "-P", "-I", "-L", "-s", "-d", "-a", "-E", "--max-args", "--max-procs", "--replace", "--max-lines", "--delimiter", "--arg-file"), empty: Read},
	"timeout":    {valueFlags: set("-s", "-k", "--signal", "--kill-after"), empty: Unknown, emptyWhy: "timeout without a command"},
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// unwrap returns the inner argv of a wrapper invocation. handled is false
// when bin is not a wrapper. ok is false when the wrapper's own flag region
// is not static (sudo $FLAGS rm x) — the caller classifies Unknown.
func unwrap(bin string, args []Arg) (inner []Arg, handled, ok bool) {
	w, isWrapper := wrappers[bin]
	if !isWrapper {
		return nil, false, false
	}
	i := 0
	sawDuration := false
	for i < len(args) {
		a := args[i]
		if !a.Static {
			return nil, true, false
		}
		switch {
		case a.Value == "--":
			return args[i+1:], true, true
		case strings.HasPrefix(a.Value, "-") && a.Value != "-":
			if w.valueFlags[a.Value] && !strings.Contains(a.Value, "=") {
				i++ // consume the value
			}
			i++
		case bin == "env" && isAssignment(a.Value):
			i++
		default:
			if bin == "timeout" && !sawDuration {
				sawDuration = true
				i++
				continue
			}
			return args[i:], true, true
		}
	}
	return nil, true, true
}

// leadingFlag reports whether one of wants appears among the leading
// static flag arguments.
func leadingFlag(args []Arg, wants ...string) bool {
	for _, a := range args {
		if !a.Static || !strings.HasPrefix(a.Value, "-") {
			return false
		}
		for _, w := range wants {
			if a.Value == w {
				return true
			}
		}
	}
	return false
}

func isAssignment(s string) bool {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	for i, c := range s[:eq] {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// classify assigns a class to a simple command's argv. Wrappers and
// string-shells are unwrapped; everything else is looked up in the table.
func (a *analyzer) classify(argv []Arg) (Class, []string) {
	return a.classifyVia(argv, nil)
}

func (a *analyzer) classifyVia(argv []Arg, via []string) (Class, []string) {
	if len(argv) == 0 {
		return Read, nil
	}
	if !argv[0].Static {
		return Unknown, append(via, "non-literal command word")
	}
	bin := resolveBin(argv[0].Value)
	if bin == "" {
		return Unknown, append(via, "unknown command "+argv[0].Value)
	}

	switch {
	case stringShells[bin]:
		return a.stringShell(bin, argv[1:], via)
	case bin == "eval":
		return a.eval(argv[1:], via)
	case bin == "find":
		return a.find(argv[1:], via)
	}

	// Two wrappers whose flags change what they are: sudo -e edits a file,
	// command -v looks a name up.
	switch {
	case bin == "sudo" && leadingFlag(argv[1:], "-e", "--edit"):
		return FSWrite, append(via, "sudoedit")
	case bin == "command" && leadingFlag(argv[1:], "-v", "-V"):
		return Read, append(via, "command -v lookup")
	}

	if inner, handled, ok := unwrap(bin, argv[1:]); handled {
		via = append(via, "via "+bin)
		switch {
		case !ok:
			return Unknown, append(via, "non-literal "+bin+" arguments")
		case len(inner) == 0:
			w := wrappers[bin]
			if w.empty == Unknown {
				return Unknown, append(via, w.emptyWhy)
			}
			return w.empty, via
		}
		return a.classifyVia(inner, via)
	}

	if fn, ok := specials[bin]; ok {
		if c, why, handled := fn(argv); handled {
			return c, append(via, why...)
		}
	}
	c, why := a.table.Lookup(bin, argv[1:])
	return c, append(via, why...)
}

// stringShell handles `bash -c 'script'`: the literal script is analysed
// one level deeper and its segments spliced in; this segment itself is
// read (the nested segments carry the classes). Anything else — a script
// file, stdin, a non-literal string — is Unknown.
func (a *analyzer) stringShell(bin string, args []Arg, via []string) (Class, []string) {
	for i, arg := range args {
		if !arg.Static {
			return Unknown, append(via, "non-literal "+bin+" arguments")
		}
		v := arg.Value
		if v == "--" || !strings.HasPrefix(v, "-") {
			break
		}
		if strings.HasPrefix(v, "--") || !strings.Contains(v, "c") {
			continue
		}
		if i+1 >= len(args) {
			return Unknown, append(via, bin+" -c without a script")
		}
		if !args[i+1].Static {
			return Unknown, append(via, bin+" -c with non-literal script")
		}
		return a.nested(bin+" -c", args[i+1].Value, via)
	}
	return Unknown, append(via, bin+" runs a script or stdin")
}

// eval joins literal arguments into one script and analyses it.
func (a *analyzer) eval(args []Arg, via []string) (Class, []string) {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if !arg.Static {
			return Unknown, append(via, "eval with non-literal arguments")
		}
		parts = append(parts, arg.Value)
	}
	return a.nested("eval", strings.Join(parts, " "), via)
}

func (a *analyzer) nested(what, script string, via []string) (Class, []string) {
	if a.depth+1 > maxDepth {
		return Unknown, append(via, "nesting too deep")
	}
	sub := analyze(script, a.table, a.depth+1)
	if sub.Err != nil {
		return Unknown, append(via, fmt.Sprintf("%s script failed to parse: %v", what, sub.Err))
	}
	a.segs = append(a.segs, sub.Segments...)
	return Read, append(via, fmt.Sprintf("%s script: %d nested command(s)", what, len(sub.Segments)))
}

// find is read on its own; each -exec/-execdir/-ok/-okdir clause is an
// inner command, -delete deletes, and -fprint* / -fls write.
func (a *analyzer) find(args []Arg, via []string) (Class, []string) {
	c := Read
	why := append(via, []string(nil)...)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !arg.Static {
			continue
		}
		switch arg.Value {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && !(args[j].Static && (args[j].Value == ";" || args[j].Value == "+")) {
				j++
			}
			ic, iwhy := a.classifyVia(args[i+1:j], []string{"via find " + arg.Value})
			c |= ic
			why = append(why, iwhy...)
			i = j
		case "-delete":
			c |= FSDelete
			why = append(why, "find -delete")
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			c |= FSWrite
			why = append(why, "find "+arg.Value+" writes a file")
		}
	}
	return c, why
}
