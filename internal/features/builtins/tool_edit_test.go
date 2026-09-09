package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

func TestApplyEdit(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		old, new   string
		replaceAll bool
		want       string
		wantErr    string
	}{
		{name: "single", content: "a\nb\nc\n", old: "b", new: "B", want: "a\nB\nc\n"},
		{name: "replace all", content: "x x x", old: "x", new: "y", replaceAll: true, want: "y y y"},
		{name: "not found", content: "a\n", old: "zzz", wantErr: "old_string not found"},
		{name: "not unique", content: "x x", old: "x", wantErr: "old_string not unique: 2 occurrences"},
		{name: "not unique is fine with replace_all", content: "x x", old: "x", new: "y", replaceAll: true, want: "y y"},
		{name: "deletion", content: "a\nb\n", old: "b\n", new: "", want: "a\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyEdit(tt.content, tt.old, tt.new, tt.replaceAll)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A successful edit reports its counts to the model and carries the diff in
// metadata; a failed one carries neither.
func TestEditToolDiffMetadata(t *testing.T) {
	run := func(t *testing.T, content, args string) core.ToolResult {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := NewEditTool(nil).Execute(context.Background(), tooldef.ExecutionContext{
			WorkingDir: dir,
			Arguments:  []string{strings.ReplaceAll(args, "$PATH", path)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	t.Run("success carries the diff", func(t *testing.T) {
		res := run(t, "one\ntwo\nthree\n", `{"file_path":"$PATH","old_string":"two","new_string":"TWO"}`)
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Output)
		}
		if !strings.HasSuffix(res.Output, "+1 -1") {
			t.Errorf("output = %q, want it to end with the counts", res.Output)
		}
		if res.Metadata["diff_added"] != "1" || res.Metadata["diff_removed"] != "1" {
			t.Errorf("metadata = %v", res.Metadata)
		}
		if !strings.Contains(res.Metadata["diff"], "+TWO") {
			t.Errorf("diff = %q", res.Metadata["diff"])
		}
	})

	t.Run("failure carries no diff", func(t *testing.T) {
		res := run(t, "one\n", `{"file_path":"$PATH","old_string":"nope","new_string":"x"}`)
		if !res.IsError {
			t.Fatal("want an error result")
		}
		if len(res.Metadata) != 0 {
			t.Errorf("metadata = %v, want none", res.Metadata)
		}
	})
}
