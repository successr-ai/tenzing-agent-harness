package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(prefix)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestDiffFiles(t *testing.T) {
	tests := []struct {
		name          string
		old, new      string
		wantAdded     int
		wantRemoved   int
		wantOmitted   string
		wantInText    []string
		wantNotInText []string
	}{
		{
			name: "single line replaced",
			old:  "one\ntwo\nthree\n", new: "one\nTWO\nthree\n",
			wantAdded: 1, wantRemoved: 1,
			wantInText: []string{"-two", "+TWO", " one", " three", "@@"},
		},
		{
			name: "pure addition",
			old:  "one\n", new: "one\ntwo\n",
			wantAdded: 1, wantRemoved: 0,
			wantInText: []string{"+two"},
		},
		{
			name: "pure deletion",
			old:  "one\ntwo\n", new: "one\n",
			wantAdded: 0, wantRemoved: 1,
			wantInText: []string{"-two"},
		},
		{
			name: "new file is all additions",
			old:  "", new: "one\ntwo\n",
			wantAdded: 2, wantRemoved: 0,
			wantInText: []string{"+one", "+two"},
		},
		{
			name: "file headers are not counted as changes",
			old:  "a\n", new: "b\n",
			wantAdded: 1, wantRemoved: 1,
			wantInText: []string{"--- a/f.go", "+++ b/f.go"},
		},
		{
			name: "no trailing newline",
			old:  "one\ntwo", new: "one\nTWO",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name: "crlf content still diffs",
			old:  "one\r\ntwo\r\n", new: "one\r\nTWO\r\n",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name: "identical content yields an empty diff",
			old:  "one\n", new: "one\n",
			wantAdded: 0, wantRemoved: 0,
		},
		{
			name: "binary is omitted",
			old:  "one\n", new: "one\x00two\n",
			wantOmitted: "binary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DiffFiles("f.go", []byte(tt.old), []byte(tt.new))
			if d.Added != tt.wantAdded || d.Removed != tt.wantRemoved {
				t.Errorf("counts = +%d -%d, want +%d -%d", d.Added, d.Removed, tt.wantAdded, tt.wantRemoved)
			}
			if d.Omitted != tt.wantOmitted {
				t.Errorf("Omitted = %q, want %q", d.Omitted, tt.wantOmitted)
			}
			if d.Omitted != "" && d.Text != "" {
				t.Error("want no Text alongside an Omitted reason")
			}
			for _, want := range tt.wantInText {
				if !strings.Contains(d.Text, want) {
					t.Errorf("diff missing %q:\n%s", want, d.Text)
				}
			}
		})
	}
}

// The hard limit drops the body; the counts survive it.
func TestDiffFilesHardLimit(t *testing.T) {
	t.Run("just under the limit keeps its body", func(t *testing.T) {
		// 40 replaced lines with no context between them: 40+40 changes plus
		// headers is over 50, so use a size that lands just under.
		d := DiffFiles("f.go", []byte(lines(20, "old")), []byte(lines(20, "new")))
		if d.Omitted != "" {
			t.Fatalf("Omitted = %q, want a kept diff", d.Omitted)
		}
		if d.Added != 20 || d.Removed != 20 {
			t.Errorf("counts = +%d -%d, want +20 -20", d.Added, d.Removed)
		}
	})

	t.Run("over the limit drops the body but keeps counts", func(t *testing.T) {
		d := DiffFiles("f.go", []byte(lines(40, "old")), []byte(lines(40, "new")))
		if d.Omitted != "too large" {
			t.Fatalf("Omitted = %q, want \"too large\"", d.Omitted)
		}
		if d.Text != "" {
			t.Error("want the body dropped")
		}
		if d.Added != 40 || d.Removed != 40 {
			t.Errorf("counts = +%d -%d, want +40 -40", d.Added, d.Removed)
		}
	})

	t.Run("an oversized file is never diffed", func(t *testing.T) {
		big := lines(maxDiffFileLines+1, "x")
		d := DiffFiles("f.go", []byte(big), []byte(big+"more\n"))
		if d.Omitted != "file too large" {
			t.Errorf("Omitted = %q, want \"file too large\"", d.Omitted)
		}
	})
}

// Inline is the collapsed-vs-expanded hint for the UI.
func TestDiffInline(t *testing.T) {
	short := DiffFiles("f.go", []byte("one\ntwo\n"), []byte("one\nTWO\n"))
	if !short.Inline() {
		t.Errorf("want a short diff inline, got %d lines:\n%s", strings.Count(short.Text, "\n"), short.Text)
	}

	long := DiffFiles("f.go", []byte(lines(15, "old")), []byte(lines(15, "new")))
	if long.Omitted != "" {
		t.Fatalf("Omitted = %q, want a kept diff", long.Omitted)
	}
	if long.Inline() {
		t.Error("want a mid-sized diff collapsed")
	}

	if (Diff{Omitted: "too large"}).Inline() {
		t.Error("want an omitted diff not inline")
	}
}

func TestDiffSummaryAndMetadata(t *testing.T) {
	d := Diff{Text: "body", Added: 3, Removed: 1}
	if got := d.Summary(); got != "+3 -1" {
		t.Errorf("Summary() = %q, want %q", got, "+3 -1")
	}
	m := diffMetadata(d)
	if m["diff"] != "body" || m["diff_added"] != "3" || m["diff_removed"] != "1" {
		t.Errorf("metadata = %v", m)
	}
	if _, ok := m["diff_omitted"]; ok {
		t.Error("want no diff_omitted for a kept diff")
	}

	m = diffMetadata(Diff{Added: 40, Removed: 40, Omitted: "too large"})
	if _, ok := m["diff"]; ok {
		t.Error("want no diff body when omitted")
	}
	if m["diff_omitted"] != "too large" || m["diff_added"] != "40" {
		t.Errorf("metadata = %v", m)
	}
}

// PreviewDiff must agree with what the tools actually do, and must not write.
func TestPreviewDiff(t *testing.T) {
	write := func(t *testing.T, content string) (dir, path string) {
		t.Helper()
		dir = t.TempDir()
		path = filepath.Join(dir, "f.txt")
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir, path
	}

	t.Run("edit matches what the tool applies", func(t *testing.T) {
		dir, path := write(t, "one\ntwo\nthree\n")
		args := `{"file_path":"` + path + `","old_string":"two","new_string":"TWO"}`

		preview, ok, err := PreviewDiff(dir, "Edit", args)
		if !ok || err != nil {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}

		res, err := NewEditTool(nil).Execute(context.Background(), tooldef.ExecutionContext{
			WorkingDir: dir, Arguments: []string{args},
		})
		if err != nil || res.IsError {
			t.Fatalf("edit failed: %v %s", err, res.Output)
		}
		if preview.Text != res.Metadata["diff"] {
			t.Errorf("preview and applied diff differ:\n%s\n---\n%s", preview.Text, res.Metadata["diff"])
		}
	})

	t.Run("preview does not write", func(t *testing.T) {
		dir, path := write(t, "one\n")
		if _, _, err := PreviewDiff(dir, "Write", `{"file_path":"`+path+`","content":"changed\n"}`); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "one\n" {
			t.Errorf("file = %q, err = %v; want it unchanged", data, err)
		}
	})

	t.Run("new file previews as all additions", func(t *testing.T) {
		dir, path := write(t, "")
		d, _, err := PreviewDiff(dir, "Write", `{"file_path":"`+path+`","content":"a\nb\n"}`)
		if err != nil {
			t.Fatal(err)
		}
		if d.Added != 2 || d.Removed != 0 {
			t.Errorf("counts = +%d -%d, want +2 -0", d.Added, d.Removed)
		}
	})

	t.Run("errors", func(t *testing.T) {
		dir, path := write(t, "one\n")
		tests := []struct{ name, tool, args, wantErr string }{
			{"other tool", "bash", `{"command":"ls"}`, ""},
			{"bad json", "Edit", `{`, "invalid input JSON"},
			{"no file_path", "Edit", `{}`, "file_path is required"},
			{"missing file", "Edit", `{"file_path":"` + dir + `/nope","old_string":"a","new_string":"b"}`, "cannot read file"},
			{"old_string not found", "Edit", `{"file_path":"` + path + `","old_string":"zzz","new_string":"b"}`, "old_string not found"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, ok, err := PreviewDiff(dir, tt.tool, tt.args)
				if tt.wantErr == "" {
					if ok {
						t.Error("want ok=false for a tool with no preview")
					}
					return
				}
				if !ok {
					t.Fatal("want ok=true")
				}
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
				}
			})
		}
	})
}
