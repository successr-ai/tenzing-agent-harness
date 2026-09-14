package shell

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestWrappers(t *testing.T) {
	tests := []struct {
		command string
		want    Class
		nseg    int
		why     string // substring expected in some Why
	}{
		{`sudo rm -rf x`, FSDelete, 1, "via sudo"},
		{`sudo -u bob ls`, Read, 1, "via sudo"},
		{`sudo -i`, Unknown, 1, "shell"},
		{`sudo -e /etc/hosts`, FSWrite, 1, "sudoedit"},
		{`sudo $FLAGS rm x`, Unknown, 1, "non-literal"},
		{`env FOO=1 go build ./...`, Read, 1, "via env"},
		{`env`, Read, 1, ""},
		{`env -i FOO=1 rm x`, FSDelete, 1, ""},
		{`xargs rm`, FSDelete, 1, "via xargs"},
		{`xargs -0 -n1 rm -f`, FSDelete, 1, ""},
		{`xargs`, Read, 1, ""},
		{`find . -name '*.go' -exec rm {} \;`, FSDelete, 1, "via find -exec"},
		{`find . -name '*.go' -exec cat {} +`, Read, 1, ""},
		{`find . -delete`, FSDelete, 1, "find -delete"},
		{`find . -exec cat {} + -exec rm {} \;`, FSDelete, 1, ""},
		{`find . -type f`, Read, 1, ""},
		{`bash -c "rm -rf /tmp/x"`, FSDelete, 2, "nested"},
		{`bash -lc 'ls'`, Read, 2, ""},
		{`sh -c 'ls; rm x'`, FSDelete, 3, ""},
		{`bash -c "$X"`, Unknown, 1, "non-literal script"},
		{`bash script.sh`, Unknown, 1, "script or stdin"},
		{`bash`, Unknown, 1, ""},
		{`bash -c 'ls "x'`, Unknown, 1, "failed to parse"},
		{`eval "rm x"`, FSDelete, 2, ""},
		{`eval rm x`, FSDelete, 2, ""},
		{`eval "$CMD"`, Unknown, 1, "non-literal"},
		{`timeout 5 curl x`, Net, 1, "via timeout"},
		{`timeout -s KILL 5 rm x`, FSDelete, 1, ""},
		{`nice -n 5 ls`, Read, 1, ""},
		{`nohup ./server &`, Unknown, 1, ""},
		{`time go test ./...`, Read, 1, ""},
		{`command -v go`, Read, 1, ""},
		{`exec rm x`, FSDelete, 1, ""},
		{`ssh host rm -rf /`, Net, 1, ""},
		{`sudo env FOO=1 rm x`, FSDelete, 1, ""},
		{`source x.sh`, Unknown, 1, ""},
		{`. x.sh`, Unknown, 1, ""},
	}
	tbl := Builtin()
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			a := Analyze(tt.command, tbl)
			if a.Err != nil {
				t.Fatal(a.Err)
			}
			if got := a.Class(); got != tt.want {
				t.Errorf("class = %s, want %s\n%s", got, tt.want, dump(a))
			}
			if len(a.Segments) != tt.nseg {
				t.Errorf("segments = %d, want %d\n%s", len(a.Segments), tt.nseg, dump(a))
			}
			if tt.why != "" && !strings.Contains(dump(a), tt.why) {
				t.Errorf("missing why %q in\n%s", tt.why, dump(a))
			}
		})
	}
}

func TestNestingCap(t *testing.T) {
	cmd := "ls"
	for i := 0; i < maxDepth+1; i++ {
		q, err := syntax.Quote(cmd, syntax.LangBash)
		if err != nil {
			t.Fatal(err)
		}
		cmd = "bash -c " + q
	}
	a := Analyze(cmd, Builtin())
	if a.Err != nil {
		t.Fatal(a.Err)
	}
	if !a.Class().Has(Unknown) || !strings.Contains(dump(a), "nesting too deep") {
		t.Errorf("expected nesting cap:\n%s", dump(a))
	}
	// One level fewer is fine.
	cmd = "ls"
	for i := 0; i < maxDepth; i++ {
		q, _ := syntax.Quote(cmd, syntax.LangBash)
		cmd = "bash -c " + q
	}
	if a := Analyze(cmd, Builtin()); !a.Class().IsRead() {
		t.Errorf("depth %d should analyse:\n%s", maxDepth, dump(a))
	}
}

func TestNestedDepth(t *testing.T) {
	a := Analyze(`bash -c "rm x"`, Builtin())
	if len(a.Segments) != 2 || a.Segments[1].Depth != 1 || a.Segments[1].Text != "rm x" {
		t.Fatalf("got %s", dump(a))
	}
}
