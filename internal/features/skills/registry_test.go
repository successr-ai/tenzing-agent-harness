package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: test skill %s\n---\nbody\n", name, name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSkipsUnreadableDirs(t *testing.T) {
	good := t.TempDir()
	writeSkill(t, good, "alpha")

	r := NewRegistry()
	r.RegisterSkillDir(filepath.Join(t.TempDir(), "does-not-exist")) // registered first, must not abort the scan
	r.RegisterSkillDir(good)

	if _, err := r.Load("alpha"); err != nil {
		t.Fatalf("skill in later dir not discovered past a bad dir: %v", err)
	}
}

func TestDiscoverKeysByDirectoryName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "on-disk-name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: frontmatter-name\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	r.RegisterSkillDir(root)

	if _, err := r.Load("on-disk-name"); err != nil {
		t.Errorf("skill not keyed by its directory: %v", err)
	}
	if _, err := r.Load("frontmatter-name"); err == nil {
		t.Error("skill registered under its frontmatter name, want directory name only")
	}
}

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
		wantErr  bool
	}{
		{
			name:     "single line",
			content:  "---\nname: alpha\ndescription: does alpha things\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "does alpha things",
		},
		{
			name:     "folded multi-line description",
			content:  "---\nname: alpha\ndescription: >-\n  Use this when the task\n  needs alpha handling.\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "Use this when the task needs alpha handling.",
		},
		{
			name:     "quoted values",
			content:  "---\nname: \"alpha\"\ndescription: \"quoted: with colon\"\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "quoted: with colon",
		},
		{
			name:     "extra keys ignored",
			content:  "---\nname: alpha\nversion: 2\ndescription: d\nallowed-tools:\n  - Bash\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "d",
		},
		{
			name:     "unquoted description with colon",
			content:  "---\nname: alpha\ndescription: Use when planning: (1) foo, (2) bar\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "Use when planning: (1) foo, (2) bar",
		},
		{
			name:     "unquoted description with colon, name last",
			content:  "---\ndescription: Trigger: any time\nname: alpha\n---\nbody\n",
			wantName: "alpha",
			wantDesc: "Trigger: any time",
		},
		{
			name:    "no frontmatter",
			content: "body only\n",
			wantErr: true,
		},
		{
			name:    "unclosed frontmatter",
			content: "---\nname: alpha\n",
			wantErr: true,
		},
		{
			name:    "missing name",
			content: "---\ndescription: d\n---\nbody\n",
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			content: "---\nname: [unclosed\n---\nbody\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SKILL.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			name, desc, err := parseFrontmatter(path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got name=%q desc=%q", name, desc)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
		})
	}
}

func TestRegisterSkillDirExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, "skills"), "beta")

	r := NewRegistry()
	r.RegisterSkillDir("~/skills")

	if _, err := r.Load("beta"); err != nil {
		t.Fatalf("tilde dir not expanded/discovered: %v", err)
	}
}
