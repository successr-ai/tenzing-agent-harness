package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadContextFiles(t *testing.T) {
	home := redirectHome(t)

	// global file under the redirected config dir
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir")
	}
	if !strings.HasPrefix(configDir, home) {
		t.Skipf("config dir %q not under redirected home %q", configDir, home)
	}
	globalDir := filepath.Join(configDir, "tenzing")
	os.MkdirAll(globalDir, 0o755)
	os.WriteFile(filepath.Join(globalDir, "AGENTS.md"), []byte("GLOBAL-RULES"), 0o644)

	// project tree: root AGENTS.md + nested AGENTS.md
	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT-RULES"), 0o644)
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("NESTED-RULES"), 0o644)

	got := loadContextFiles(sub)
	out, loaded := got.content, got.files

	for _, want := range []string{"GLOBAL-RULES", "ROOT-RULES", "NESTED-RULES"} {
		if !strings.Contains(out, want) {
			t.Errorf("context files missing %q", want)
		}
	}
	// order: global, then root→cwd
	gi := strings.Index(out, "GLOBAL-RULES")
	ri := strings.Index(out, "ROOT-RULES")
	ni := strings.Index(out, "NESTED-RULES")
	if !(gi < ri && ri < ni) {
		t.Errorf("order wrong: global=%d root=%d nested=%d", gi, ri, ni)
	}
	// path headers present
	if !strings.Contains(out, filepath.Join(root, "AGENTS.md")) {
		t.Error("missing path header for root AGENTS.md")
	}

	// loaded paths mirror the appended content, same order, no misses
	want := []string{
		filepath.Join(globalDir, "AGENTS.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(sub, "AGENTS.md"),
	}
	if !slices.Equal(loaded, want) {
		t.Errorf("loaded = %v, want %v", loaded, want)
	}
}

func TestLoadContextFilesLoadsClaudeMD(t *testing.T) {
	home := redirectHome(t)
	globalClaude := filepath.Join(home, ".claude")
	os.MkdirAll(globalClaude, 0o755)
	os.WriteFile(filepath.Join(globalClaude, "CLAUDE.md"), []byte("GLOBAL-CLAUDE"), 0o644)

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("PROJECT-AGENTS"), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("PROJECT-CLAUDE"), 0o644)

	got := loadContextFiles(root)
	out, loaded := got.content, got.files

	for _, want := range []string{"GLOBAL-CLAUDE", "PROJECT-AGENTS", "PROJECT-CLAUDE"} {
		if !strings.Contains(out, want) {
			t.Errorf("context files missing %q", want)
		}
	}
	// global before project, and AGENTS.md before CLAUDE.md within a directory
	gi := strings.Index(out, "GLOBAL-CLAUDE")
	ai := strings.Index(out, "PROJECT-AGENTS")
	ci := strings.Index(out, "PROJECT-CLAUDE")
	if !(gi < ai && ai < ci) {
		t.Errorf("order wrong: globalClaude=%d agents=%d claude=%d", gi, ai, ci)
	}
	want := []string{
		filepath.Join(globalClaude, "CLAUDE.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "CLAUDE.md"),
	}
	if !slices.Equal(loaded, want) {
		t.Errorf("loaded = %v, want %v", loaded, want)
	}
}

func TestLoadContextFilesExpandsImports(t *testing.T) {
	redirectHome(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("@RULES.md\ntail line\n"), 0o644)
	os.WriteFile(filepath.Join(root, "RULES.md"), []byte("@nested/DEEP.md"), 0o644)
	os.MkdirAll(filepath.Join(root, "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "nested", "DEEP.md"), []byte("DEEP-RULES"), 0o644)

	got := loadContextFiles(root)
	out, loaded := got.content, got.files

	if !strings.Contains(out, "DEEP-RULES") {
		t.Errorf("transitive import not inlined; got %q", out)
	}
	if strings.Contains(out, "@RULES.md") {
		t.Error("import line left unexpanded")
	}
	if !strings.Contains(out, "tail line") {
		t.Error("content after an import line was dropped")
	}
	want := []string{
		filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, "RULES.md"),
		filepath.Join(root, "nested", "DEEP.md"),
	}
	if !slices.Equal(loaded, want) {
		t.Errorf("loaded = %v, want %v", loaded, want)
	}
}

func TestLoadContextFilesImportEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		claude  string
		want    string
		notWant string
	}{
		{
			name:    "missing import left literal",
			claude:  "@absent.md",
			want:    "@absent.md",
			notWant: "# Imported from",
		},
		{
			name:    "mention with spaces is not an import",
			claude:  "ping @someone about it",
			want:    "ping @someone about it",
			notWant: "# Imported from",
		},
		{
			name:    "self-import does not recurse forever",
			claude:  "@CLAUDE.md",
			want:    "@CLAUDE.md",
			notWant: "# Imported from",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redirectHome(t)
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(tt.claude), 0o644)

			out := loadContextFiles(root).content // must terminate
			if !strings.Contains(out, tt.want) {
				t.Errorf("missing %q in %q", tt.want, out)
			}
			if strings.Contains(out, tt.notWant) {
				t.Errorf("unexpected %q in %q", tt.notWant, out)
			}
		})
	}
}

func TestLoadContextFilesLoadsRules(t *testing.T) {
	home := redirectHome(t)
	rules := filepath.Join(home, ".claude", "rules")
	os.MkdirAll(rules, 0o755)
	os.WriteFile(filepath.Join(rules, "b-testing.md"), []byte("RULE-TESTING"), 0o644)
	os.WriteFile(filepath.Join(rules, "a-security.md"), []byte("RULE-SECURITY"), 0o644)
	os.WriteFile(filepath.Join(rules, "notes.txt"), []byte("NOT-MARKDOWN"), 0o644)

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("PROJECT-AGENTS"), 0o644)

	got := loadContextFiles(root)
	out, loaded := got.content, got.files

	if strings.Contains(out, "NOT-MARKDOWN") {
		t.Error("non-markdown file in rules/ was loaded")
	}
	// rules sort by filename, and all globals precede the project chain
	si := strings.Index(out, "RULE-SECURITY")
	ti := strings.Index(out, "RULE-TESTING")
	pi := strings.Index(out, "PROJECT-AGENTS")
	if si < 0 || ti < 0 {
		t.Fatalf("rules not loaded: security=%d testing=%d", si, ti)
	}
	if !(si < ti && ti < pi) {
		t.Errorf("order wrong: security=%d testing=%d project=%d", si, ti, pi)
	}
	// rules are accounted separately from context files
	wantRules := []string{
		filepath.Join(rules, "a-security.md"),
		filepath.Join(rules, "b-testing.md"),
	}
	if !slices.Equal(got.rules, wantRules) {
		t.Errorf("rules = %v, want %v", got.rules, wantRules)
	}
	wantFiles := []string{filepath.Join(root, "AGENTS.md")}
	if !slices.Equal(loaded, wantFiles) {
		t.Errorf("files = %v, want %v", loaded, wantFiles)
	}
}

func TestLoadContextFilesTruncation(t *testing.T) {
	redirectHome(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("x", contextFilesMaxBytes+1000)), 0o644)

	got := loadContextFiles(root)
	out, truncated := got.content, got.truncated
	if !truncated {
		t.Error("oversize context files not reported as truncated")
	}
	if !strings.Contains(out, "[context files truncated at 128KB]") {
		t.Error("oversize context files not truncated with marker")
	}
	if len(out) > contextFilesMaxBytes+200 {
		t.Errorf("output len = %d, want capped near %d", len(out), contextFilesMaxBytes)
	}
}

func TestLoadContextFilesEmpty(t *testing.T) {
	redirectHome(t)
	got := loadContextFiles(t.TempDir())
	out, loaded := got.content, got.files
	if out != "" {
		t.Errorf("no context file anywhere should produce empty string, got %q", out)
	}
	if len(loaded) != 0 {
		t.Errorf("no context file anywhere should load no paths, got %v", loaded)
	}
}
