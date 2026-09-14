package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions/shell"
)

// readBashRules decodes the bash rules the settings file at path holds.
func readBashRules(t *testing.T, path string) *permissions.BashRules {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Permissions map[string]*permissions.BashRules `json:"permissions"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	_, rules, _ := LookupTool(f.Permissions, BashKey)
	if rules == nil {
		return permissions.NewBashRules(nil, nil)
	}
	return rules
}

// BashAllowStore persists to the settings file and updates the live rules.
func TestBashAllowStoreAdd(t *testing.T) {
	newStore := func(t *testing.T, body string) *BashAllowStore {
		t.Helper()
		path := filepath.Join(t.TempDir(), "settings.json")
		rules := permissions.NewBashRules(nil, nil)
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			rules = readBashRules(t, path)
		}
		return NewBashAllowStore(path, rules)
	}

	t.Run("creates a missing file and applies live", func(t *testing.T) {
		s := newStore(t, "")
		if err := s.Add("git *"); err != nil {
			t.Fatal(err)
		}
		if d, _, ok := s.Rules().Verdict("git commit -m x"); !ok || d != 0 {
			t.Errorf("Verdict = (%v, %v), want an allow", d, ok)
		}
		reloaded := readBashRules(t, s.Path())
		allow, _ := reloaded.Lists()
		if len(allow) != 1 || allow[0] != "git *" {
			t.Errorf("persisted allow = %v, want [git *]", allow)
		}
	})

	t.Run("appends without dropping deny or other keys", func(t *testing.T) {
		s := newStore(t, `{"permissions":{"bash":{"allow":["ls *"],"deny":["rm *"]},"write":{"allow":["*"]}},"future":{"k":1}}`)
		if err := s.Add("git *"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if _, ok := doc["future"]; !ok {
			t.Error("want unknown top-level keys preserved")
		}
		var perms map[string]json.RawMessage
		if err := json.Unmarshal(doc["permissions"], &perms); err != nil {
			t.Fatal(err)
		}
		if _, ok := perms["write"]; !ok {
			t.Error("want other tools under permissions preserved")
		}
		reloaded := readBashRules(t, s.Path())
		allow, deny := reloaded.Lists()
		if len(allow) != 2 || allow[0] != "ls *" || allow[1] != "git *" {
			t.Errorf("allow = %v, want [ls * git *]", allow)
		}
		if len(deny) != 1 || deny[0] != "rm *" {
			t.Errorf("deny = %v, want [rm *]", deny)
		}
	})

	t.Run("duplicate is a no-op", func(t *testing.T) {
		s := newStore(t, `{"permissions":{"bash":{"allow":["ls *"]}}}`)
		if err := s.Add("ls *"); err != nil {
			t.Fatal(err)
		}
		reloaded := readBashRules(t, s.Path())
		if allow, _ := reloaded.Lists(); len(allow) != 1 {
			t.Errorf("allow = %v, want one entry", allow)
		}
	})

	t.Run("malformed file errors without changing the live rules", func(t *testing.T) {
		s := newStore(t, "")
		if err := os.WriteFile(s.Path(), []byte(`{"permissions":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := s.Add("git *"); err == nil {
			t.Fatal("want an error")
		}
		if d, _, _ := s.Rules().Verdict("git commit -m x"); d == 0 {
			t.Error("want the live rules untouched after a failed write")
		}
	})

	t.Run("round-trips categories and classify, never session grants", func(t *testing.T) {
		s := newStore(t, `{"permissions":{"bash":{"allow":["ls *"],"deny":[],
		  "categories":{"allow":["vcs"],"deny":["fs:delete"]},
		  "classify":{"mytool":"read"}}}}`)
		s.Rules().AllowPatternSession("./tmp.sh *")
		if err := s.Add("git *"); err != nil {
			t.Fatal(err)
		}
		reloaded := readBashRules(t, s.Path())
		allow, _ := reloaded.Lists()
		if len(allow) != 2 || allow[0] != "ls *" || allow[1] != "git *" {
			t.Errorf("allow = %v, want [ls * git *] (no session grant)", allow)
		}
		if ca, cd := reloaded.Categories(); ca != shell.VCS || cd != shell.FSDelete {
			t.Errorf("categories = (%s, %s), want (vcs, fs:delete)", ca, cd)
		}
		if got := reloaded.Classify(); got["mytool"] != "read" {
			t.Errorf("classify = %v", got)
		}
		if d, _, ok := s.Rules().Verdict("./tmp.sh"); !ok || d != 0 {
			t.Errorf("session grant lost from the live rules: %v %v", d, ok)
		}
	})
}
