package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// handleSuggest proposes the "allow always" glob for a pending bash call
// without answering the approval.
func TestHandleSuggest(t *testing.T) {
	setup := func(t *testing.T, tool, input string, allow []string, withStore bool) *agentServer {
		t.Helper()
		s := &agentServer{approvals: map[string]pendingApproval{
			"c1": {respond: func(bool) { t.Error("suggest must not answer the approval") },
				tool: tool, input: input},
		}}
		if withStore {
			rules := permissions.NewBashRules(allow, nil)
			s.bashAllow = &bashAllowStore{path: filepath.Join(t.TempDir(), "settings.json"), rules: rules}
		}
		return s
	}

	call := func(t *testing.T, s *agentServer, id string) *suggestOutput {
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
		s := setup(t, "bash", `{"command":"grep -n x f | head -3"}`, []string{"grep *"}, true)
		if out := call(t, s, "c1"); out.Body.Glob != "head *" || out.Body.Reason != "" {
			t.Errorf("glob = %q, reason = %q; want \"head *\"", out.Body.Glob, out.Body.Reason)
		}
		if len(s.approvals) != 1 {
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
	s := &agentServer{approvals: map[string]pendingApproval{
		"c1": {respond: func(bool) {}, tool: "Bash", input: `{"command":"head -3 f"}`},
	}}
	s.bashAllow = &bashAllowStore{
		path:  filepath.Join(t.TempDir(), "settings.json"),
		rules: permissions.NewBashRules(nil, nil),
	}
	in := &suggestInput{}
	in.Body.CallID = "c1"
	out, err := s.handleSuggest(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Body.Glob != "head *" {
		t.Errorf("glob = %q, reason = %q; want \"head *\"", out.Body.Glob, out.Body.Reason)
	}
}
