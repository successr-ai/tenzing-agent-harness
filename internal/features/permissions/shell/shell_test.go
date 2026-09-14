package shell

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

type want struct {
	text  string
	class Class
	depth int
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []want
		wantErr bool
	}{
		{"simple read", `ls -la`, []want{{"ls -la", Read, 0}}, false},
		{"assignment prefix stripped", `FOO=1 go build ./... 2>&1 | head`, []want{{"go build ./... 2>&1", Read, 0}, {"head", Read, 0}}, false},
		{"redirect write", `echo hi > out.txt`, []want{{"echo hi >out.txt", FSWrite, 0}}, false},
		{"redirect append", `echo hi >> out.txt`, []want{{"echo hi >>out.txt", FSWrite, 0}}, false},
		{"redirect devnull", `echo hi > /dev/null`, []want{{"echo hi >/dev/null", Read, 0}}, false},
		{"dup stderr", `cmd 2>&1`, []want{{"cmd 2>&1", Unknown, 0}}, false},
		{"dup to 2", `echo x >&2`, []want{{"echo x >&2", Read, 0}}, false},
		{"dup to file", `echo x >& log`, []want{{"echo x >&log", FSWrite, 0}}, false},
		{"rdrall", `echo x &> log`, []want{{"echo x &>log", FSWrite, 0}}, false},
		{"nonliteral redirect", `echo x > $OUT`, []want{{"echo x >$OUT", FSWrite, 0}}, false},
		{"cmdsubst", `echo "$(rm -rf x)"`, []want{{`echo "$(rm -rf x)"`, Read, 0}, {"rm -rf x", FSDelete, 1}}, false},
		{"backtick", "echo `cat f`", []want{{"echo $(cat f)", Read, 0}, {"cat f", Read, 1}}, false},
		{"procsubst", `diff <(ls a) <(ls b)`, []want{{"diff <(ls a) <(ls b)", Read, 0}, {"ls a", Read, 1}, {"ls b", Read, 1}}, false},
		{"for loop", `for f in *.go; do rm "$f"; done`, []want{{`rm "$f"`, FSDelete, 0}}, false},
		{"compound redirect", `{ echo a; echo b; } > out`, []want{{"echo a", Read, 0}, {"echo b", Read, 0}, {"{ echo a; echo b; } >out", FSWrite, 0}}, false},
		{"function", `f() { rm x; }; f`, []want{{"rm x", FSDelete, 0}, {"f", Unknown, 0}}, false},
		{"test clause", `[[ -f x ]] && echo yes`, []want{{"echo yes", Read, 0}}, false},
		{"heredoc subst", "cat <<EOF\n$(rm x)\nEOF", []want{{"cat <<EOF\n$(rm x)\nEOF", Read, 0}, {"rm x", FSDelete, 1}}, false},
		{"parse error", `ls 'unterminated`, nil, true},
		{"nonliteral command", `$CMD --flag`, []want{{"$CMD --flag", Unknown, 0}}, false},
		{"assignment only", `X=1`, nil, false},
		{"assignment with subst", `X=$(rm y)`, []want{{"rm y", FSDelete, 1}}, false},
		{"and or chain", `ls && rm x || echo fail`, []want{{"ls", Read, 0}, {"rm x", FSDelete, 0}, {"echo fail", Read, 0}}, false},
		{"pipeline", `cat f | grep x | wc -l`, []want{{"cat f", Read, 0}, {"grep x", Read, 0}, {"wc -l", Read, 0}}, false},
		{"background", `sleep 1 & ls`, []want{{"sleep 1", Read, 0}, {"ls", Read, 0}}, false},
		{"negated", `! grep -q x f`, []want{{"grep -q x f", Read, 0}}, false},
		{"subshell", `(cd dir && rm x)`, []want{{"cd dir", Read, 0}, {"rm x", FSDelete, 0}}, false},
		{"if", `if test -f x; then rm x; else echo no; fi`, []want{{"test -f x", Read, 0}, {"rm x", FSDelete, 0}, {"echo no", Read, 0}}, false},
		{"while", `while read l; do echo $l; done < f`, []want{{"read l", Read, 0}, {"echo $l", Read, 0}}, false},
		{"negated background stripped", `! rm x &`, []want{{"rm x", FSDelete, 0}}, false},
		{"case", `case $x in a) rm a;; *) ls;; esac`, []want{{"rm a", FSDelete, 0}, {"ls", Read, 0}}, false},
		{"export", `export PATH=$PATH:/x`, nil, false},
		{"absolute path", `/usr/bin/ls`, []want{{"/usr/bin/ls", Read, 0}}, false},
		{"relative path", `./ls`, []want{{"./ls", Unknown, 0}}, false},
		{"tee write", `ls | tee out`, []want{{"ls", Read, 0}, {"tee out", FSWrite, 0}}, false},
		{"tee devnull", `ls | tee /dev/null`, []want{{"ls", Read, 0}, {"tee /dev/null", Read, 0}}, false},
		{"raw keeps assignment", `LD_PRELOAD=x ls`, []want{{"ls", Read, 0}}, false},
	}
	tbl := Builtin()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Analyze(tt.command, tbl)
			if (got.Err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", got.Err, tt.wantErr)
			}
			if tt.wantErr {
				if !syntax.IsIncomplete(got.Err) {
					t.Errorf("expected incomplete parse error, got %v", got.Err)
				}
				if len(got.Segments) != 0 {
					t.Errorf("segments on parse error: %+v", got.Segments)
				}
				return
			}
			if len(got.Segments) != len(tt.want) {
				t.Fatalf("got %d segments, want %d:\n%s", len(got.Segments), len(tt.want), dump(got))
			}
			for i, w := range tt.want {
				g := got.Segments[i]
				if g.Text != w.text || g.Class != w.class || g.Depth != w.depth {
					t.Errorf("segment %d = {%q %s d%d}, want {%q %s d%d} (why: %v)", i, g.Text, g.Class, g.Depth, w.text, w.class, w.depth, g.Why)
				}
			}
		})
	}
}

func TestStaticUnescape(t *testing.T) {
	tests := []struct {
		command string
		want    string // Argv[1].Value
	}{
		{`echo \;`, ";"},
		{`echo "a\"b"`, `a"b`},
		{`echo "a\$b"`, "a$b"},
		{`echo "a\nb"`, `a\nb`},
		{`echo 'a\"b'`, `a\"b`},
		{`echo "x\\y"`, `x\y`},
	}
	for _, tt := range tests {
		a := Analyze(tt.command, Builtin())
		if a.Err != nil || len(a.Segments) != 1 || len(a.Segments[0].Argv) != 2 {
			t.Fatalf("%q: %v %s", tt.command, a.Err, dump(a))
		}
		if got := a.Segments[0].Argv[1]; !got.Static || got.Value != tt.want {
			t.Errorf("%q: argv[1] = %+v, want %q", tt.command, got, tt.want)
		}
	}
}

func TestRawKeepsAssignments(t *testing.T) {
	a := Analyze(`LD_PRELOAD=x ls`, Builtin())
	if len(a.Segments) != 1 || a.Segments[0].Raw != "LD_PRELOAD=x ls" || a.Segments[0].Text != "ls" {
		t.Fatalf("got %+v", a.Segments)
	}
}

func TestSummary(t *testing.T) {
	tbl := Builtin()
	tests := []struct {
		command string
		want    []string // substrings
	}{
		{`ls | head`, []string{"read-only"}},
		{`ls; rm x`, []string{"fs:delete", "rm x", "rm: fs:delete"}},
		{`ls 'x`, []string{"parse error"}},
		{`echo > f && rm y`, []string{"fs:write", "redirect > f", "fs:delete", "rm y"}},
	}
	for _, tt := range tests {
		got := Analyze(tt.command, tbl).Summary()
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("Summary(%q) = %q, missing %q", tt.command, got, w)
			}
		}
	}
}

func TestClassString(t *testing.T) {
	tests := []struct {
		c    Class
		want string
	}{
		{Read, "read"},
		{FSWrite, "fs:write"},
		{FSWrite | Net, "fs:write,net"},
		{FSDelete | VCS | Unknown, "fs:delete,vcs,unknown"},
	}
	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.c, got, tt.want)
		}
		if tt.c.Has(Unknown) {
			continue
		}
		back, err := ParseClass(tt.want)
		if err != nil || back != tt.c {
			t.Errorf("ParseClass(%q) = %v, %v; want %v", tt.want, back, err, tt.c)
		}
	}
	for _, bad := range []string{"unknown", "fs:write,unknown", "bogus", ""} {
		if _, err := ParseClass(bad); err == nil {
			t.Errorf("ParseClass(%q) accepted", bad)
		}
	}
}

func dump(a Analysis) string {
	var b strings.Builder
	for _, s := range a.Segments {
		b.WriteString(s.Text + " | " + s.Class.String() + " | " + strings.Join(s.Why, "; ") + "\n")
	}
	return b.String()
}
