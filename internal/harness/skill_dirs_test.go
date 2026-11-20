package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultSkillDirsDisabled: with WithDefaultSkillDirsDisabled the
// host's ~/.claude/skills is not scanned, while a folder given with
// WithSkillsDir still is; without it the default folder is scanned.
func TestDefaultSkillDirsDisabled(t *testing.T) {
	writeSkill := func(dir, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(opts ...HarnessOption) map[string]string {
		t.Helper()
		h, err := New(&stubLLM{}, append([]HarnessOption{
			WithAgentBuilder(stubBrain), WithSystemPrompt("test"), WithContextFilesDisabled(),
		}, opts...)...)
		if err != nil {
			t.Fatalf("New() error: %v", err)
		}
		t.Cleanup(h.Shutdown)
		return h.Skills()
	}

	home := redirectHome(t)
	writeSkill(filepath.Join(home, ".claude", "skills"), "host-skill")
	extra := t.TempDir()
	writeSkill(extra, "app-skill")

	if got := build(WithSkillsDir(extra)); got["host-skill"] == "" || got["app-skill"] == "" {
		t.Errorf("default: skills %v, want the host's and the app's", got)
	}
	got := build(WithDefaultSkillDirsDisabled(), WithSkillsDir(extra))
	if _, ok := got["host-skill"]; ok {
		t.Errorf("disabled: the host's ~/.claude/skills was scanned: %v", got)
	}
	if got["app-skill"] == "" {
		t.Errorf("disabled: an explicit WithSkillsDir folder was dropped: %v", got)
	}
}
