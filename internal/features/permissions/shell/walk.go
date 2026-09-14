package shell

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// maxDepth caps nesting of $(…), bash -c and eval. Past it a command is
// Unknown rather than analysed.
const maxDepth = 4

// Analyze parses command as bash and returns its simple commands, each
// classified against t. A parse error yields Analysis{Err} with no
// segments; a command that runs nothing (assignments only) yields no
// segments and no error.
func Analyze(command string, t Table) Analysis {
	return analyze(command, t, 0)
}

func analyze(command string, t Table, depth int) Analysis {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return Analysis{Err: err}
	}
	a := &analyzer{table: t, depth: depth, printer: syntax.NewPrinter(syntax.SingleLine(true))}
	a.stmts(f.Stmts)
	return Analysis{Segments: a.segs}
}

type analyzer struct {
	table   Table
	printer *syntax.Printer
	segs    []Segment
	depth   int
}

func (a *analyzer) stmts(ss []*syntax.Stmt) {
	for _, s := range ss {
		a.stmt(s)
	}
}

// stmt handles one statement: a simple command becomes a segment (with its
// redirects folded in); a compound command is recursed into, and a write
// redirect on it becomes a synthetic segment of its own.
func (a *analyzer) stmt(s *syntax.Stmt) {
	if call, ok := s.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
		idx := len(a.segs)
		seg := a.call(s, call)
		for _, r := range s.Redirs {
			a.redirect(r, &seg)
		}
		// Nested substitutions were appended while evaluating the words;
		// the enclosing command goes in front of them.
		a.segs = append(a.segs[:idx], append([]Segment{seg}, a.segs[idx:]...)...)
		return
	}

	a.command(s.Cmd)

	var synth Segment
	for _, r := range s.Redirs {
		a.redirect(r, &synth)
	}
	if !synth.Class.IsRead() {
		synth.Text = a.print(s)
		synth.Raw = synth.Text
		synth.Depth = a.depth
		synth.Why = append([]string{"redirect on compound command"}, synth.Why...)
		a.segs = append(a.segs, synth)
	}
}

// command recurses into a non-simple command, collecting segments from
// nested statements and from substitutions in any words it holds.
func (a *analyzer) command(c syntax.Command) {
	switch x := c.(type) {
	case nil:
	case *syntax.CallExpr: // assignments only: executes nothing
		a.subst(x)
	case *syntax.IfClause:
		a.stmts(x.Cond)
		a.stmts(x.Then)
		if x.Else != nil {
			a.command(x.Else)
		}
	case *syntax.WhileClause:
		a.stmts(x.Cond)
		a.stmts(x.Do)
	case *syntax.ForClause:
		a.subst(x.Loop)
		a.stmts(x.Do)
	case *syntax.CaseClause:
		a.subst(x.Word)
		for _, it := range x.Items {
			for _, p := range it.Patterns {
				a.subst(p)
			}
			a.stmts(it.Stmts)
		}
	case *syntax.Block:
		a.stmts(x.Stmts)
	case *syntax.Subshell:
		a.stmts(x.Stmts)
	case *syntax.BinaryCmd:
		a.stmt(x.X)
		a.stmt(x.Y)
	case *syntax.FuncDecl:
		a.stmt(x.Body)
	case *syntax.TimeClause:
		if x.Stmt != nil {
			a.stmt(x.Stmt)
		}
	case *syntax.CoprocClause:
		if x.Stmt != nil {
			a.stmt(x.Stmt)
		}
	default:
		// DeclClause, TestClause, ArithmCmd, LetClause: shell-internal, no
		// segment — but their words may still hold substitutions.
		a.subst(x)
	}
}

// subst walks any node for command and process substitutions, analysing
// their bodies one level deeper. It does not descend into a substitution
// it has handled (the nested statements are walked by stmts).
func (a *analyzer) subst(n syntax.Node) {
	if n == nil {
		return
	}
	syntax.Walk(n, func(n syntax.Node) bool {
		var body []*syntax.Stmt
		switch x := n.(type) {
		case *syntax.CmdSubst:
			body = x.Stmts
		case *syntax.ProcSubst:
			body = x.Stmts
		default:
			return true
		}
		a.depth++
		a.stmts(body)
		a.depth--
		return false
	})
}

// call builds the segment for a simple command. Text and Raw are printed
// from the statement so redirects stay part of what a glob sees (a deny
// glob can target `>/etc/*`; an allow glob must spell `>` to cover a
// redirect write — see permissions.snapshot.covered), minus the `&`/`!`
// decorations.
func (a *analyzer) call(s *syntax.Stmt, c *syntax.CallExpr) Segment {
	a.subst(c)
	argv := make([]Arg, len(c.Args))
	for i, w := range c.Args {
		v, ok := static(w)
		argv[i] = Arg{Value: v, Static: ok}
	}
	bare := *s
	bare.Background, bare.Negated, bare.Coprocess, bare.Comments = false, false, false, nil
	stripped := *c
	stripped.Assigns = nil
	withAssigns := bare
	bare.Cmd = &stripped
	seg := Segment{
		Text:  a.print(&bare),
		Raw:   a.print(&withAssigns),
		Argv:  argv,
		Depth: a.depth,
	}
	seg.Class, seg.Why = a.classify(argv)
	return seg
}

// devSinks are redirect targets that write nowhere new.
var devSinks = map[string]bool{
	"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/tty": true,
}

// redirect folds one redirect's classification into seg. Input redirects
// and descriptor duplications (2>&1, >&2) are reads; here-doc bodies are
// still walked for substitutions.
func (a *analyzer) redirect(r *syntax.Redirect, seg *Segment) {
	if r.Word != nil {
		a.subst(r.Word)
	}
	if r.Hdoc != nil {
		a.subst(r.Hdoc)
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
	case syntax.DplOut:
		// `2>&1` / `>&2` / `>&-` duplicate a descriptor; bash reads `>& word`
		// with anything else as `&> word`.
		if v, ok := static(r.Word); ok && (v == "-" || isDigits(v)) {
			return
		}
	default:
		return
	}
	target, ok := static(r.Word)
	switch {
	case !ok:
		seg.Class |= FSWrite
		seg.Why = append(seg.Why, "redirect "+r.Op.String()+" to non-literal path")
	case devSinks[target] || strings.HasPrefix(target, "/dev/fd/"):
	default:
		seg.Class |= FSWrite
		seg.Why = append(seg.Why, "redirect "+r.Op.String()+" "+target)
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// static evaluates a word without a shell: literal and quoted parts only.
// Any expansion (parameter, command, arithmetic, glob, brace) makes the
// word non-static. Note (*Word).Lit alone is not enough — it returns "" for
// anything quoted.
func static(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(x.Value, false))
		case *syntax.SglQuoted:
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unescape(l.Value, true))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// unescape resolves backslash escapes the way bash would: unquoted, a
// backslash quotes any next character; inside double quotes only $ ` " \
// and newline are special.
func unescape(s string, inDouble bool) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch {
		case next == '\n':
			i++ // line continuation
		case !inDouble || strings.IndexByte("$`\"\\", next) >= 0:
			b.WriteByte(next)
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (a *analyzer) print(n syntax.Node) string {
	var b strings.Builder
	if err := a.printer.Print(&b, n); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}
