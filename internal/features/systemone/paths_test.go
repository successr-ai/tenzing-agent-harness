package systemone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// pathExt builds an extension rooted at work, with a shell splitter that
// behaves like the harness's (whitespace-free words only — quoting is the
// real parser's job, and is tested where that parser lives).
func pathExt(t *testing.T, work string) *Ext {
	t.Helper()
	ext := New(Config{Client: &fakeJudge{}, WorkingDir: work})
	ext.SetShellSplitter(func(cmd string) []string { return strings.Fields(cmd) })
	return ext
}

func TestClassifyPathPlacesALocation(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // a sibling temp dir, so also inside the temp root
	ext := pathExt(t, work)

	tests := []struct {
		name        string
		in          string
		wantOutside bool
	}{
		{"a relative path stays inside", "sub/file.go", false},
		{"the working directory itself is inside", ".", false},
		{"an absolute path inside stays inside", filepath.Join(work, "a.txt"), false},
		{"dot-dot escapes", "../elsewhere/x.txt", true},
		{"a sibling directory is outside", filepath.Join(outside, "x.txt"), true},
		{"an unrelated absolute path is outside", "/etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ext.classifyPath(tt.in)
			if got.OutsideWorkingDirectory != tt.wantOutside {
				t.Fatalf("%s → outside=%v, want %v (resolved %s, work %s)",
					tt.in, got.OutsideWorkingDirectory, tt.wantOutside, got.Path, ext.workdir)
			}
			if !filepath.IsAbs(got.Path) {
				t.Fatalf("path must be resolved to absolute, got %q", got.Path)
			}
		})
	}
}

// The system temp directory is reached through a symlink on macOS (/tmp →
// /private/tmp). Comparing unresolved paths reports the temp directory as
// somewhere else entirely, so both sides are resolved.
func TestInTempDirectorySeesThroughSymlinks(t *testing.T) {
	ext := pathExt(t, t.TempDir())

	viaTempDir := ext.classifyPath(filepath.Join(os.TempDir(), "scratch.txt"))
	if !viaTempDir.InTempDirectory {
		t.Fatalf("os.TempDir() must read as temp: %+v", viaTempDir)
	}
	if ext.classifyPath("/etc/passwd").InTempDirectory {
		t.Fatal("/etc is not the temp directory")
	}
}

// A project that happens to live under the temp root is still the project:
// nothing inside the working directory is scratch, or deleting the user's
// untracked work reads as tidying up.
func TestWorkingDirectoryIsNeverTemp(t *testing.T) {
	work := t.TempDir() // under os.TempDir()
	ext := pathExt(t, work)
	for _, p := range []string{".", "backup", "sub/deep/file.txt"} {
		got := ext.classifyPath(p)
		if got.InTempDirectory || got.OutsideWorkingDirectory {
			t.Fatalf("%s inside a temp-rooted workdir = %+v, want inside and not temp", p, got)
		}
	}
	sibling := filepath.Join(filepath.Dir(work), "elsewhere", "x.txt")
	if got := ext.classifyPath(sibling); !got.InTempDirectory || !got.OutsideWorkingDirectory {
		t.Fatalf("a sibling under the temp root = %+v, want outside and temp", got)
	}
}

func TestResolveFollowsSymlinksForPathsThatDoNotExistYet(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := resolve(filepath.Join(link, "new", "file.txt"), root)
	want := filepath.Join(followSymlinks(real), "new", "file.txt")
	if got != want {
		t.Fatalf("resolve = %q, want %q", got, want)
	}
}

func TestPathsForReadsNativeArgumentsAndShellOperands(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "backup"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ext := pathExt(t, work)

	tests := []struct {
		name  string
		call  core.ToolCall
		want  []string // resolved paths, in order
		check func(t *testing.T, facts []pathFact)
	}{
		{
			name: "a native path argument",
			call: core.ToolCall{Name: "Read", Input: `{"file_path":"sub/x.go"}`},
			want: []string{filepath.Join(work, "sub/x.go")},
		},
		{
			name: "a bare shell operand that names something real",
			call: core.ToolCall{Name: "bash", Input: `{"command":"rm -r backup"}`},
			want: []string{filepath.Join(work, "backup")},
		},
		{
			name: "flags are not paths",
			call: core.ToolCall{Name: "bash", Input: `{"command":"ls -la"}`},
			want: nil,
		},
		{
			name: "a bare word naming nothing is not a path",
			call: core.ToolCall{Name: "bash", Input: `{"command":"git status"}`},
			want: nil,
		},
		{
			name: "an absolute operand outside is reported as outside",
			call: core.ToolCall{Name: "bash", Input: `{"command":"cat /etc/passwd"}`},
			want: []string{"/etc/passwd"},
			check: func(t *testing.T, facts []pathFact) {
				if !facts[0].OutsideWorkingDirectory {
					t.Fatal("/etc/passwd must read as outside the working directory")
				}
			},
		},
		{
			name: "a long flag's attached value is a path",
			call: core.ToolCall{Name: "bash", Input: `{"command":"cp x --target-directory=/etc"}`},
			want: []string{"/etc"},
		},
		{
			name: "a glob pattern is the location it searches",
			call: core.ToolCall{Name: "Glob", Input: `{"pattern":"/etc/**/*.conf"}`},
			want: []string{"/etc/**/*.conf"},
		},
		{
			name: "a grep pattern is a regex, not a path",
			call: core.ToolCall{Name: "Grep", Input: `{"pattern":"/api/v1"}`},
			want: nil,
		},
		{
			name: "paths written inside inline code are found",
			call: core.ToolCall{Name: "bash", Input: `{"command":"python3 -c open('/etc/passwd').read()"}`},
			want: []string{"/etc/passwd"},
		},
		{
			name: "code not after a code flag is an ordinary word",
			call: core.ToolCall{Name: "bash", Input: `{"command":"git commit -m fix(/etc)"}`},
			want: []string{filepath.Join(work, "fix(/etc)")},
		},
		{
			name: "unparseable input yields nothing",
			call: core.ToolCall{Name: "bash", Input: `not json`},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := ext.pathsFor(tt.call)
			if len(facts) != len(tt.want) {
				t.Fatalf("facts = %+v, want %v", facts, tt.want)
			}
			for i, w := range tt.want {
				if facts[i].Path != followSymlinks(w) {
					t.Fatalf("path[%d] = %q, want %q", i, facts[i].Path, followSymlinks(w))
				}
			}
			if tt.check != nil {
				tt.check(t, facts)
			}
		})
	}
}

func TestPathsAreDedupedAndBounded(t *testing.T) {
	work := t.TempDir()
	ext := pathExt(t, work)

	dup := ext.pathsFor(core.ToolCall{Name: "bash", Input: `{"command":"cp ./a.txt ./a.txt"}`})
	if len(dup) != 1 {
		t.Fatalf("the same path twice is one fact, got %+v", dup)
	}

	var words []string
	for i := 0; i < maxPathFacts+5; i++ {
		words = append(words, "./f"+string(rune('a'+i))+".txt")
	}
	many := ext.pathsFor(core.ToolCall{Name: "bash", Input: `{"command":"rm ` + strings.Join(words, " ") + `"}`})
	if len(many) != maxPathFacts {
		t.Fatalf("facts = %d, want them capped at %d", len(many), maxPathFacts)
	}
}

// Without the splitter — nothing bound it — bash reports no paths rather
// than wrong ones.
func TestBashPathsAbsentWithoutASplitter(t *testing.T) {
	ext := New(Config{Client: &fakeJudge{}, WorkingDir: t.TempDir()})
	if got := ext.pathsFor(core.ToolCall{Name: "bash", Input: `{"command":"rm -rf /etc"}`}); got != nil {
		t.Fatalf("got %+v, want no facts", got)
	}
}

func TestTmpIsATempRoot(t *testing.T) {
	ext := pathExt(t, t.TempDir())
	f := ext.classifyPath("/tmp/scratch.txt")
	if !f.OutsideWorkingDirectory || !f.InTempDirectory {
		t.Fatalf("/tmp must read as outside but temp, got %+v", f)
	}
}
