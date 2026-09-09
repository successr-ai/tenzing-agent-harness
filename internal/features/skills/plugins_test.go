package skills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writePluginSkill creates <root>/<rel>/SKILL.md with the given skill name.
func writePluginSkill(t *testing.T, root, rel, name string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// claudeTree builds a Claude config dir with two installed plugins: "alpha"
// (enabled, nested skills, no allowlist) and "beta" (disabled).
func claudeTree(t *testing.T) (claudeDir, alphaRoot, betaRoot string) {
	t.Helper()
	claudeDir = t.TempDir()
	alphaRoot = filepath.Join(claudeDir, "plugins", "cache", "alpha")
	betaRoot = filepath.Join(claudeDir, "plugins", "cache", "beta")

	writeJSON(t, filepath.Join(claudeDir, "settings.json"), `{"enabledPlugins":{
		"alpha@mk": true, "beta@mk": false}}`)
	writeJSON(t, filepath.Join(claudeDir, "plugins", "installed_plugins.json"), `{"plugins":{
		"alpha@mk":[{"installPath":"`+alphaRoot+`"}],
		"beta@mk":[{"installPath":"`+betaRoot+`"}]}}`)
	writeJSON(t, filepath.Join(alphaRoot, ".claude-plugin", "plugin.json"), `{"name":"alpha"}`)
	writeJSON(t, filepath.Join(betaRoot, ".claude-plugin", "plugin.json"), `{"name":"beta"}`)
	writePluginSkill(t, betaRoot, "skills/hidden", "hidden")
	return claudeDir, alphaRoot, betaRoot
}

func TestRegisterPluginDir(t *testing.T) {
	claudeDir, alphaRoot, _ := claudeTree(t)
	writePluginSkill(t, alphaRoot, "skills/flat", "flat")
	writePluginSkill(t, alphaRoot, "skills/deep/nested", "nested")

	r := NewRegistry()
	r.RegisterPluginDir(claudeDir)
	got := r.GetSkillMap()

	for _, want := range []string{"alpha:flat", "alpha:nested"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keysOf(got))
		}
	}
	if _, ok := got["beta:hidden"]; ok {
		t.Error("disabled plugin contributed a skill")
	}
	if len(got) != 2 {
		t.Errorf("got %d skills, want 2: %v", len(got), keysOf(got))
	}
}

func TestRegisterPluginDirHonorsDeclaredSkills(t *testing.T) {
	claudeDir, alphaRoot, _ := claudeTree(t)
	writeJSON(t, filepath.Join(alphaRoot, ".claude-plugin", "plugin.json"),
		`{"name":"alpha","skills":["./skills/keep"]}`)
	writePluginSkill(t, alphaRoot, "skills/keep", "keep")
	writePluginSkill(t, alphaRoot, "skills/deprecated/drop", "drop")

	r := NewRegistry()
	r.RegisterPluginDir(claudeDir)
	got := r.GetSkillMap()

	if _, ok := got["alpha:keep"]; !ok {
		t.Errorf("declared skill missing; got %v", keysOf(got))
	}
	if _, ok := got["alpha:drop"]; ok {
		t.Error("undeclared skill registered despite an explicit allowlist")
	}
}

func TestRegisterPluginDirNamespaceAvoidsCollisions(t *testing.T) {
	claudeDir, alphaRoot, _ := claudeTree(t)
	writePluginSkill(t, alphaRoot, "skills/plan", "plan")

	standalone := t.TempDir()
	writeSkill(t, standalone, "plan")

	r := NewRegistry()
	r.RegisterSkillDir(standalone)
	r.RegisterPluginDir(claudeDir)
	got := r.GetSkillMap()

	if _, ok := got["plan"]; !ok {
		t.Error("standalone skill lost to a same-named plugin skill")
	}
	if _, ok := got["alpha:plan"]; !ok {
		t.Error("plugin skill missing its namespace")
	}
}

func TestRegisterPluginDirMissingMetadata(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name:  "no claude dir at all",
			setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
		},
		{
			name: "settings without enabledPlugins",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeJSON(t, filepath.Join(dir, "settings.json"), `{"model":"x"}`)
				return dir
			},
		},
		{
			name: "enabled plugin with no install record",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeJSON(t, filepath.Join(dir, "settings.json"), `{"enabledPlugins":{"ghost@mk":true}}`)
				writeJSON(t, filepath.Join(dir, "plugins", "installed_plugins.json"), `{"plugins":{}}`)
				return dir
			},
		},
		{
			name: "malformed settings json",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				writeJSON(t, filepath.Join(dir, "settings.json"), `{not json`)
				return dir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			r.RegisterPluginDir(tt.setup(t)) // must not panic
			if got := r.GetSkillMap(); len(got) != 0 {
				t.Errorf("got %v, want no skills", keysOf(got))
			}
		})
	}
}

func TestPluginSkillKeyedByDirectoryName(t *testing.T) {
	claudeDir, alphaRoot, _ := claudeTree(t)
	// caveman ships skills/compress/SKILL.md whose frontmatter says
	// "caveman-compress"; the directory is what Claude Code shows.
	writePluginSkill(t, alphaRoot, "skills/compress", "alpha-compress")

	r := NewRegistry()
	r.RegisterPluginDir(claudeDir)
	got := r.GetSkillMap()

	if _, ok := got["alpha:compress"]; !ok {
		t.Errorf("plugin skill not keyed by directory; got %v", keysOf(got))
	}
	if _, ok := got["alpha:alpha-compress"]; ok {
		t.Error("plugin skill registered under its frontmatter name")
	}
}

func TestPluginNamespaceFallsBackToKey(t *testing.T) {
	claudeDir, alphaRoot, _ := claudeTree(t)
	// plugin.json missing entirely -> namespace comes from "alpha@mk"
	os.Remove(filepath.Join(alphaRoot, ".claude-plugin", "plugin.json"))
	writePluginSkill(t, alphaRoot, "skills/solo", "solo")

	r := NewRegistry()
	r.RegisterPluginDir(claudeDir)

	if _, ok := r.GetSkillMap()["alpha:solo"]; !ok {
		t.Errorf("namespace fallback failed; got %v", keysOf(r.GetSkillMap()))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
