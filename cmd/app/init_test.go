package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
)

// The embedded set is what `tenzing init` promises to write.
var wantDefaults = []string{"SYSTEM_PROMPT.md", "settings.json", "tenzing.yaml"}

func TestWriteDefaultsCreatesFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tenzing")

	var out bytes.Buffer
	if err := writeDefaults(dir, &out); err != nil {
		t.Fatal(err)
	}

	for _, name := range wantDefaults {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", name)
		}
		if !strings.Contains(out.String(), "created  "+name) {
			t.Errorf("output missing %q:\n%s", name, out.String())
		}
	}
}

// Re-running must never clobber a config that may hold API keys or edits.
func TestWriteDefaultsSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	kept := filepath.Join(dir, "tenzing.yaml")
	if err := os.WriteFile(kept, []byte("model: ollama/mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := writeDefaults(dir, &out); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(kept)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "model: ollama/mine\n" {
		t.Errorf("tenzing.yaml was overwritten: %q", got)
	}
	if !strings.Contains(out.String(), "skipped  tenzing.yaml") {
		t.Errorf("output missing skip line:\n%s", out.String())
	}
	// The other two still get written.
	for _, name := range []string{"SYSTEM_PROMPT.md", "settings.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// `tenzing init` targets the same directory --config and --settings probe.
func TestInitCmdTargetsUserConfigDir(t *testing.T) {
	redirectUserConfig(t)

	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, name := range wantDefaults {
		if _, err := os.Stat(filepath.Join(userConfigDir(), name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := filepath.Dir(userConfigPath()); got != userConfigDir() {
		t.Errorf("config probe dir = %q, init wrote to %q", got, userConfigDir())
	}
}

// The embedded defaults must actually parse as the files they claim to be —
// a broken starter config would fail at the user's first run, not ours.
func TestEmbeddedDefaultsAreValid(t *testing.T) {
	dir := t.TempDir()
	if err := writeDefaults(dir, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	// Parsing is not enough: the starter config is the only source of
	// providers and models, so a first run fails unless its registry builds
	// and its model: key actually resolves.
	f, _, err := cfgfile.Load(filepath.Join(dir, "tenzing.yaml"), true)
	if err != nil {
		t.Fatalf("defaults/tenzing.yaml: %v", err)
	}
	reg, err := buildRegistry(f.Providers, f.Models)
	if err != nil {
		t.Fatalf("defaults/tenzing.yaml registry: %v", err)
	}
	if f.Model == "" {
		t.Error("defaults/tenzing.yaml sets no model:")
	}
	if _, err := reg.resolve(f.Model); err != nil {
		t.Errorf("defaults/tenzing.yaml model %q: %v", f.Model, err)
	}
	if _, err := loadSettingsFile(filepath.Join(dir, "settings.json"), true); err != nil {
		t.Errorf("defaults/settings.json: %v", err)
	}
}
