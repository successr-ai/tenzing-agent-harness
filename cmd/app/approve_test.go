package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// handleApprove answers a pending request; a non-empty `allow` persists the
// glob, applies it live, and approves the call.
func TestHandleApprove(t *testing.T) {
	type answer struct {
		got    bool
		called bool
	}

	setup := func(t *testing.T, tool string, withStore bool) (*agentServer, *answer, *bashAllowStore) {
		t.Helper()
		a := &answer{}
		s := &agentServer{approvals: map[string]pendingApproval{
			"c1": {respond: func(ok bool) { a.got, a.called = ok, true }, tool: tool},
		}}
		var store *bashAllowStore
		if withStore {
			store = &bashAllowStore{
				path:  filepath.Join(t.TempDir(), defaultSettingsPath),
				rules: permissions.NewBashRules(nil, nil),
			}
			s.bashAllow = store
		}
		return s, a, store
	}

	call := func(s *agentServer, approved bool, allow string) (*statusOutput, error) {
		in := &approveInput{}
		in.Body.CallID = "c1"
		in.Body.Approved = approved
		in.Body.Allow = allow
		return s.handleApprove(context.Background(), nil, in)
	}

	t.Run("plain approve", func(t *testing.T) {
		s, a, _ := setup(t, "bash", true)
		out, err := call(s, true, "")
		if err != nil || out.Body.Status != "approved" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		if !a.called || !a.got {
			t.Error("want the call approved")
		}
	})

	t.Run("plain deny", func(t *testing.T) {
		s, a, _ := setup(t, "bash", true)
		out, err := call(s, false, "")
		if err != nil || out.Body.Status != "denied" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		if !a.called || a.got {
			t.Error("want the call denied")
		}
	})

	t.Run("allow persists, applies live, and approves", func(t *testing.T) {
		s, a, store := setup(t, "bash", true)
		out, err := call(s, false, "  git *  ") // approved:false is overridden
		if err != nil || out.Body.Status != "allowed" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		if !a.called || !a.got {
			t.Error("want the call approved")
		}
		if _, ok := store.rules.Verdict("git status"); !ok {
			t.Error("want the pattern live for this session")
		}
		reloaded, err := loadSettingsFile(store.path, true)
		if err != nil {
			t.Fatal(err)
		}
		if allow, _ := reloaded.Lists(); len(allow) != 1 || allow[0] != "git *" {
			t.Errorf("persisted allow = %v, want [git *] (trimmed)", allow)
		}
		if len(s.approvals) != 0 {
			t.Error("want the pending request cleared")
		}
	})

	t.Run("allow is rejected for non-bash calls", func(t *testing.T) {
		s, a, _ := setup(t, "write", true)
		if _, err := call(s, true, "git *"); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("want the call left unanswered")
		}
		if len(s.approvals) != 1 {
			t.Error("want the request still pending")
		}
	})

	t.Run("allow is rejected without a settings store", func(t *testing.T) {
		s, a, _ := setup(t, "bash", false)
		if _, err := call(s, true, "git *"); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("want the call left unanswered")
		}
	})

	t.Run("blank allow is rejected", func(t *testing.T) {
		s, a, _ := setup(t, "bash", true)
		if _, err := call(s, true, "   "); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("want the call left unanswered")
		}
	})

	t.Run("a failed write leaves the request pending", func(t *testing.T) {
		s, a, store := setup(t, "bash", true)
		if err := os.WriteFile(store.path, []byte(`{"permissions":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := call(s, true, "git *"); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("want the call left unanswered so the user can retry")
		}
		if len(s.approvals) != 1 {
			t.Error("want the request still pending")
		}
		if _, ok := store.rules.Verdict("git status"); ok {
			t.Error("want the live rules untouched")
		}
	})

	t.Run("unknown call id", func(t *testing.T) {
		s, _, _ := setup(t, "bash", true)
		in := &approveInput{}
		in.Body.CallID = "nope"
		if _, err := s.handleApprove(context.Background(), nil, in); err == nil {
			t.Fatal("want an error")
		}
	})
}
