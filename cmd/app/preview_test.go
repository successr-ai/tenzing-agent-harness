package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// handlePreview reports what a pending Edit/Write would do, without applying
// it and without answering the approval.
func TestHandlePreview(t *testing.T) {
	setup := func(t *testing.T, tool, input, content string) (*agentServer, string) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		s := &agentServer{cwd: dir, approvals: map[string]pendingApproval{
			"c1": {respond: func(bool) { t.Error("preview must not answer the approval") },
				tool: tool, input: strings.ReplaceAll(input, "$PATH", path)},
		}}
		return s, path
	}

	call := func(t *testing.T, s *agentServer, id string) *previewOutput {
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
		if len(s.approvals) != 1 {
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
		if len(s.approvals) != 1 {
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
