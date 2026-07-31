package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

func jsonArg(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return string(b)
}

func execTool(t *testing.T, def tooldef.Definition, workDir string, input map[string]any) core.ToolResult {
	t.Helper()
	res, err := def.Execute(context.Background(), tooldef.ExecutionContext{
		Arguments:  []string{jsonArg(t, input)},
		WorkingDir: workDir,
	})
	if err != nil {
		t.Fatalf("%s execute: %v", def.Name(), err)
	}
	return res
}

// TestReadBeforeEditEnforcement covers the FileTracker contract across
// Read, Edit, and Write via table-driven scenarios.
func TestReadBeforeEditEnforcement(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool)
	}{
		{
			name: "edit without read rejected",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				os.WriteFile(path, []byte("hello"), 0o644)
				res := execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
				if !res.IsError || !strings.Contains(res.Output, "Read") {
					t.Fatalf("want read-first rejection, got: %v %q", res.IsError, res.Output)
				}
			},
		},
		{
			name: "read then edit ok",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				os.WriteFile(path, []byte("hello"), 0o644)
				execTool(t, read, dir, map[string]any{"file_path": path})
				res := execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
				if res.IsError {
					t.Fatalf("edit after read rejected: %q", res.Output)
				}
			},
		},
		{
			name: "read, external modify, edit rejected until re-read",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				os.WriteFile(path, []byte("hello"), 0o644)
				execTool(t, read, dir, map[string]any{"file_path": path})
				os.WriteFile(path, []byte("hello external"), 0o644)
				res := execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
				if !res.IsError || !strings.Contains(res.Output, "changed") {
					t.Fatalf("want stale rejection, got: %v %q", res.IsError, res.Output)
				}
				execTool(t, read, dir, map[string]any{"file_path": path})
				res = execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
				if res.IsError {
					t.Fatalf("edit after re-read rejected: %q", res.Output)
				}
			},
		},
		{
			name: "replace_all after read ok and restamps",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				os.WriteFile(path, []byte("aaa bbb aaa"), 0o644)
				execTool(t, read, dir, map[string]any{"file_path": path})
				res := execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "aaa", "new_string": "ccc", "replace_all": true})
				if res.IsError {
					t.Fatalf("replace_all rejected: %q", res.Output)
				}
				// stamp updated: an immediate second edit is allowed
				res = execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "bbb", "new_string": "ddd"})
				if res.IsError {
					t.Fatalf("edit after replace_all rejected: %q", res.Output)
				}
			},
		},
		{
			name: "write new file ok, no read needed",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "new", "sub", "f.txt")
				res := execTool(t, write, dir, map[string]any{"file_path": path, "content": "fresh"})
				if res.IsError {
					t.Fatalf("write new file rejected: %q", res.Output)
				}
				data, _ := os.ReadFile(path)
				if string(data) != "fresh" {
					t.Fatalf("content = %q, want %q", data, "fresh")
				}
			},
		},
		{
			name: "overwrite unread rejected, after read ok",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				os.WriteFile(path, []byte("old"), 0o644)
				res := execTool(t, write, dir, map[string]any{"file_path": path, "content": "new"})
				if !res.IsError {
					t.Fatal("overwrite of un-read file should be rejected")
				}
				execTool(t, read, dir, map[string]any{"file_path": path})
				res = execTool(t, write, dir, map[string]any{"file_path": path, "content": "new"})
				if res.IsError {
					t.Fatalf("overwrite after read rejected: %q", res.Output)
				}
			},
		},
		{
			name: "write then edit ok without separate read",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				path := filepath.Join(dir, "f.txt")
				execTool(t, write, dir, map[string]any{"file_path": path, "content": "hello"})
				res := execTool(t, edit, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
				if res.IsError {
					t.Fatalf("edit after write rejected: %q", res.Output)
				}
			},
		},
		{
			name: "relative and absolute paths share one stamp",
			run: func(t *testing.T, dir string, read *ReadTool, edit *EditTool, write *WriteTool) {
				abs := filepath.Join(dir, "f.txt")
				os.WriteFile(abs, []byte("hello"), 0o644)
				execTool(t, read, dir, map[string]any{"file_path": "f.txt"}) // relative
				res := execTool(t, edit, dir, map[string]any{"file_path": abs, "old_string": "hello", "new_string": "bye"})
				if res.IsError {
					t.Fatalf("edit via abs path after relative read rejected: %q", res.Output)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tracker := NewFileTracker()
			tt.run(t, dir, NewReadTool(tracker), NewEditTool(tracker), NewWriteTool(tracker))
		})
	}
}

// Nil tracker disables enforcement (zero-value tools stay usable).
func TestEditWithoutTrackerUnenforced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("hello"), 0o644)
	res := execTool(t, &EditTool{}, dir, map[string]any{"file_path": path, "old_string": "hello", "new_string": "bye"})
	if res.IsError {
		t.Fatalf("nil-tracker edit rejected: %q", res.Output)
	}
}

// Concurrent Edits and direct writes on the same path must be race-free
// (pathLocks interplay); run with -race.
func TestEditConcurrentWithExternalWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	tracker := NewFileTracker()
	read := NewReadTool(tracker)
	edit := NewEditTool(tracker)

	readArg := jsonArg(t, map[string]any{"file_path": path})
	editArg := jsonArg(t, map[string]any{"file_path": path, "old_string": "hello", "new_string": "hello"})
	exctx := tooldef.ExecutionContext{Arguments: []string{readArg}, WorkingDir: dir}
	editCtx := tooldef.ExecutionContext{Arguments: []string{editArg}, WorkingDir: dir}

	// Results are irrelevant (stale rejections are expected); the test is
	// that -race stays clean while Edit races direct writes.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, _ = read.Execute(context.Background(), exctx)
			_, _ = edit.Execute(context.Background(), editCtx)
		})
		wg.Go(func() {
			_ = os.WriteFile(path, []byte("hello"), 0o644)
		})
	}
	wg.Wait()
}
