package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// pendingServer builds a harness-less Server with one pending approval.
func pendingServer(t *testing.T, callID string, p approvals.Pending) *Server {
	t.Helper()
	s := New(ServerConfig{})
	s.approvals.Add(callID, p)
	return s
}

// persistedBashAllow reads back the bash allow list the store wrote.
func persistedBashAllow(t *testing.T, path string) []string {
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
	allow, _ := f.Permissions["bash"].Lists()
	return allow
}

// handleApprove answers a pending request; a non-empty `allow` persists the
// glob, applies it live, and approves the call.
func TestHandleApprove(t *testing.T) {
	type answer struct {
		got    bool
		called bool
	}

	setup := func(t *testing.T, tool string, withStore bool) (*Server, *answer, *app.BashAllowStore) {
		t.Helper()
		a := &answer{}
		s := pendingServer(t, "c1", approvals.Pending{Respond: func(ok bool) { a.got, a.called = ok, true }, Tool: tool})
		var store *app.BashAllowStore
		if withStore {
			store = app.NewBashAllowStore(filepath.Join(t.TempDir(), "settings.json"), permissions.NewBashRules(nil, nil))
			s.cfg.BashAllow = store
		}
		return s, a, store
	}

	call := func(s *Server, approved bool, allow string) (*statusOutput, error) {
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
		if d, _, ok := store.Rules().Verdict("git commit -m x"); !ok || d != 0 {
			t.Error("want the pattern live for this session")
		}
		if allow := persistedBashAllow(t, store.Path()); len(allow) != 1 || allow[0] != "git *" {
			t.Errorf("persisted allow = %v, want [git *] (trimmed)", allow)
		}
		if s.approvals.Len() != 0 {
			t.Error("want the pending request cleared")
		}
	})

	t.Run("session scope applies live without persisting", func(t *testing.T) {
		s, a, store := setup(t, "bash", true)
		in := &approveInput{}
		in.Body.CallID, in.Body.Allow, in.Body.Scope = "c1", "git *", "session"
		out, err := s.handleApprove(context.Background(), nil, in)
		if err != nil || out.Body.Status != "allowed_session" {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		if !a.called || !a.got {
			t.Error("want the call approved")
		}
		if d, _, ok := store.Rules().Verdict("git commit -m x"); !ok || d != 0 {
			t.Error("want the pattern live for this session")
		}
		if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
			t.Errorf("settings file should not exist after a session grant (stat err = %v)", err)
		}
		if got := store.Rules().SessionList(); len(got) != 1 || got[0] != "git *" {
			t.Errorf("SessionList = %v", got)
		}
	})

	t.Run("unknown scope is rejected", func(t *testing.T) {
		s, a, _ := setup(t, "bash", true)
		in := &approveInput{}
		in.Body.CallID, in.Body.Allow, in.Body.Scope = "c1", "git *", "forever"
		if _, err := s.handleApprove(context.Background(), nil, in); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("call must stay pending")
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
		if s.approvals.Len() != 1 {
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
		if err := os.WriteFile(store.Path(), []byte(`{"permissions":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := call(s, true, "git *"); err == nil {
			t.Fatal("want an error")
		}
		if a.called {
			t.Error("want the call left unanswered so the user can retry")
		}
		if s.approvals.Len() != 1 {
			t.Error("want the request still pending")
		}
		if d, _, _ := store.Rules().Verdict("git commit -m x"); d == 0 {
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

// handlePreview reports what a pending Edit/Write would do, without applying
// it and without answering the approval.
func TestHandlePreview(t *testing.T) {
	setup := func(t *testing.T, tool, input, content string) (*Server, string) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		s := pendingServer(t, "c1", approvals.Pending{
			Respond: func(bool) { t.Error("preview must not answer the approval") },
			Tool:    tool, Input: strings.ReplaceAll(input, "$PATH", path)})
		s.cfg.Cwd = dir
		return s, path
	}

	call := func(t *testing.T, s *Server, id string) *previewOutput {
		t.Helper()
		in := &previewInput{}
		in.Body.CallID = id
		out, err := s.handlePreview(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("edit", func(t *testing.T) {
		s, path := setup(t, "Edit", `{"file_path":"$PATH","old_string":"two","new_string":"TWO"}`, "one\ntwo\n")
		out := call(t, s, "c1")
		if out.Body.Added != 1 || out.Body.Removed != 1 {
			t.Errorf("counts = +%d -%d, want +1 -1", out.Body.Added, out.Body.Removed)
		}
		if !strings.Contains(out.Body.Diff, "+TWO") {
			t.Errorf("diff = %q", out.Body.Diff)
		}
		// The file must be untouched: preview never writes.
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "one\ntwo\n" {
			t.Errorf("file = %q, err = %v; want it unchanged", data, err)
		}
		if s.approvals.Len() != 1 {
			t.Error("want the request still pending")
		}
	})

	t.Run("write over an existing file", func(t *testing.T) {
		s, _ := setup(t, "Write", `{"file_path":"$PATH","content":"one\nTWO\n"}`, "one\ntwo\n")
		out := call(t, s, "c1")
		if out.Body.Added != 1 || out.Body.Removed != 1 || out.Body.Error != "" {
			t.Errorf("out = %+v", out.Body)
		}
	})

	t.Run("write of a new file is all additions", func(t *testing.T) {
		s, _ := setup(t, "Write", `{"file_path":"$PATH","content":"one\ntwo\n"}`, "")
		out := call(t, s, "c1")
		if out.Body.Added != 2 || out.Body.Removed != 0 || out.Body.Error != "" {
			t.Errorf("out = %+v", out.Body)
		}
	})

	t.Run("unpreviewable tool reports why", func(t *testing.T) {
		s, _ := setup(t, "bash", `{"command":"ls"}`, "")
		if out := call(t, s, "c1"); out.Body.Error == "" {
			t.Error("want an error explaining there is no preview")
		}
	})

	t.Run("a failing edit reports why", func(t *testing.T) {
		s, _ := setup(t, "Edit", `{"file_path":"$PATH","old_string":"nope","new_string":"x"}`, "one\n")
		out := call(t, s, "c1")
		if !strings.Contains(out.Body.Error, "old_string not found") {
			t.Errorf("error = %q", out.Body.Error)
		}
		if s.approvals.Len() != 1 {
			t.Error("want the request still pending")
		}
	})

	t.Run("unknown call id", func(t *testing.T) {
		s, _ := setup(t, "Edit", `{}`, "")
		in := &previewInput{}
		in.Body.CallID = "nope"
		if _, err := s.handlePreview(context.Background(), nil, in); err == nil {
			t.Fatal("want an error")
		}
	})
}

// handleSuggest proposes the "allow always" glob for a pending bash call
// without answering the approval.
func TestHandleSuggest(t *testing.T) {
	setup := func(t *testing.T, tool, input string, allow []string, withStore bool) *Server {
		t.Helper()
		s := pendingServer(t, "c1", approvals.Pending{
			Respond: func(bool) { t.Error("suggest must not answer the approval") },
			Tool:    tool, Input: input})
		if withStore {
			rules := permissions.NewBashRules(allow, nil)
			s.cfg.BashAllow = app.NewBashAllowStore(filepath.Join(t.TempDir(), "settings.json"), rules)
		}
		return s
	}

	call := func(t *testing.T, s *Server, id string) *suggestOutput {
		t.Helper()
		in := &suggestInput{}
		in.Body.CallID = id
		out, err := s.handleSuggest(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("first uncovered expression", func(t *testing.T) {
		s := setup(t, "bash", `{"command":"./a.sh -n x | ./b.sh -3"}`, []string{"./a.sh *"}, true)
		if out := call(t, s, "c1"); out.Body.Glob != "./b.sh *" || out.Body.Reason != "" {
			t.Errorf("glob = %q, reason = %q; want \"./b.sh *\"", out.Body.Glob, out.Body.Reason)
		}
		if s.approvals.Len() != 1 {
			t.Error("want the request still pending")
		}
	})

	t.Run("no glob for a non-bash call", func(t *testing.T) {
		s := setup(t, "Edit", `{"file_path":"f"}`, nil, true)
		if out := call(t, s, "c1"); out.Body.Glob != "" || out.Body.Reason != "no glob for Edit" {
			t.Errorf("glob = %q, reason = %q", out.Body.Glob, out.Body.Reason)
		}
	})

	t.Run("no store configured", func(t *testing.T) {
		s := setup(t, "bash", `{"command":"ls"}`, nil, false)
		if out := call(t, s, "c1"); out.Body.Reason != "no settings file configured for allow" {
			t.Errorf("reason = %q", out.Body.Reason)
		}
	})

	t.Run("unparseable input suggests nothing", func(t *testing.T) {
		s := setup(t, "bash", `not json`, nil, true)
		if out := call(t, s, "c1"); out.Body.Glob != "" || out.Body.Reason == "" {
			t.Errorf("glob = %q, reason = %q", out.Body.Glob, out.Body.Reason)
		}
	})

	t.Run("unknown call id", func(t *testing.T) {
		s := setup(t, "bash", `{"command":"ls"}`, nil, true)
		in := &suggestInput{}
		in.Body.CallID = "nope"
		if _, err := s.handleSuggest(context.Background(), nil, in); err == nil {
			t.Error("want an error for an unknown call_id")
		}
	})
}

// The pending call's tool name is matched case-insensitively: a harness that
// registers the tool as "Bash" still gets a glob suggestion.
func TestHandleSuggestToolNameCase(t *testing.T) {
	s := pendingServer(t, "c1", approvals.Pending{Respond: func(bool) {}, Tool: "Bash", Input: `{"command":"./b.sh -3 f"}`})
	s.cfg.BashAllow = app.NewBashAllowStore(filepath.Join(t.TempDir(), "settings.json"), permissions.NewBashRules(nil, nil))
	in := &suggestInput{}
	in.Body.CallID = "c1"
	out, err := s.handleSuggest(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Glob != "./b.sh *" {
		t.Errorf("glob = %q, reason = %q; want \"./b.sh *\"", out.Body.Glob, out.Body.Reason)
	}
}
