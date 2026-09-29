package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGrepToolBoundsOutput(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    []string // substrings the output must contain
		notWant []string // substrings it must not
		maxLen  int      // 0 = no length check
	}{
		{
			name:  "short match kept whole",
			files: map[string]string{"a.txt": "hello needle world"},
			want:  []string{"a.txt:1: hello needle world"},
		},
		{
			name:   "long line truncated",
			files:  map[string]string{"min.js.map": "needle" + strings.Repeat("x", 1<<20)},
			want:   []string{"min.js.map:1: needle", "…"},
			maxLen: 1024,
		},
		{
			name:  "multibyte line cut on a rune boundary",
			files: map[string]string{"u.txt": "needle" + strings.Repeat("é", maxGrepLineBytes)},
			want:  []string{"…"},
		},
		{
			name: "total output capped",
			files: func() map[string]string {
				m := map[string]string{}
				for i := range 400 { // 400 truncated lines ≈ 120 KB uncapped
					m[fmt.Sprintf("d/f%03d.txt", i)] = "needle " + strings.Repeat("y", maxGrepLineBytes)
				}
				return m
			}(),
			want:   []string{"[truncated at 64 KB]"},
			maxLen: maxGrepBytes + 1024,
		},
		{
			name: "node_modules skipped",
			files: map[string]string{
				"src/app.ts":              "needle in src",
				"node_modules/dep/dep.js": "needle in dep",
			},
			want:    []string{"needle in src"},
			notWant: []string{"needle in dep"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			res := execTool(t, &GrepTool{}, dir, map[string]any{"pattern": "needle"})
			if res.IsError {
				t.Fatalf("grep error: %q", res.Output)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Output, w) {
					t.Errorf("output missing %q:\n%.500s", w, res.Output)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(res.Output, nw) {
					t.Errorf("output contains %q:\n%.500s", nw, res.Output)
				}
			}
			if tt.maxLen > 0 && len(res.Output) > tt.maxLen {
				t.Errorf("output is %d bytes, want <= %d", len(res.Output), tt.maxLen)
			}
			if !utf8.ValidString(res.Output) {
				t.Errorf("output is not valid UTF-8")
			}
		})
	}
}
