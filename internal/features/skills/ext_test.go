package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "goskill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: goskill\ndescription: Go language guidance\n---\nUse table-driven tests.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.RegisterSkillDir(dir)
	return reg
}

func TestPromptFragmentListsSkills(t *testing.T) {
	ext := NewExt(newTestRegistry(t))
	frag := ext.PromptFragment()
	if !strings.Contains(frag, "Available skills") {
		t.Errorf("fragment missing header: %q", frag)
	}
	if !strings.Contains(frag, "goskill: Go language guidance") {
		t.Errorf("fragment missing skill line: %q", frag)
	}
}

func TestPromptFragmentEmptyRegistry(t *testing.T) {
	ext := NewExt(NewRegistry())
	if frag := ext.PromptFragment(); frag != "" {
		t.Errorf("expected empty fragment, got %q", frag)
	}
}

func TestToolsAreListAndLoad(t *testing.T) {
	ext := NewExt(newTestRegistry(t))
	specs := ext.Tools()
	if len(specs) != 2 {
		t.Fatalf("expected 2 tool specs, got %d", len(specs))
	}
	byName := map[string]core.ToolSpec{}
	for _, s := range specs {
		byName[s.Definition.Name] = s
		if s.Origin != "extension:skills" {
			t.Errorf("spec %s origin = %q, want extension:skills", s.Definition.Name, s.Origin)
		}
	}

	listSpec, ok := byName["list_skills"]
	if !ok {
		t.Fatal("missing list_skills spec")
	}
	res := listSpec.Execute(context.Background(), core.ToolCall{ID: "c1", Name: "list_skills", Input: "{}"})
	if res.IsError || !strings.Contains(res.Output, "goskill") {
		t.Errorf("list_skills output = %q (err=%v)", res.Output, res.IsError)
	}

	loadSpec, ok := byName["load_skill"]
	if !ok {
		t.Fatal("missing load_skill spec")
	}
	res = loadSpec.Execute(context.Background(), core.ToolCall{ID: "c2", Name: "load_skill", Input: `{"name":"goskill"}`})
	if res.IsError || !strings.Contains(res.Output, "=== SKILL: goskill ===") {
		t.Errorf("load_skill output = %q (err=%v)", res.Output, res.IsError)
	}
	if res.ToolUseID != "c2" {
		t.Errorf("ToolUseID = %q, want c2", res.ToolUseID)
	}
}
