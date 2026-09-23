package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/modelregistry"
	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"go.yaml.in/yaml/v3"
)

// soRegistryYAML declares both kinds of model, two of the chat ones
// described, so routing has a choice to make.
const soRegistryYAML = "providers:\n" +
	"  - name: local\n    type: ollama\n    url: http://localhost:11434\n" +
	"  - name: jev-backend\n    type: systemone\n    url: https://api.typesafe.ai\n    api_key: test-key\n" +
	"models:\n" +
	"  llm:\n" +
	"    - name: alpha\n      provider: local\n      model_name: glm-5.3\n      description: Frontier. Refactors.\n" +
	"    - name: beta\n      provider: local\n      model_name: qwen3\n      description: Cheap. Lookups.\n" +
	"    - name: plain\n      provider: local\n      model_name: q2\n" +
	"  systemone:\n" +
	"    - name: jev\n      provider: jev-backend\n      model_name: jev-latest\n"

func soFile(t *testing.T) cfgfile.File {
	t.Helper()
	var f cfgfile.File
	if err := yaml.Unmarshal([]byte(soRegistryYAML), &f); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return f
}

// soDeps builds deps that can resolve both kinds from soRegistryYAML.
func soDeps(t *testing.T) *deps {
	t.Helper()
	f := soFile(t)
	reg, err := modelregistry.Build(f.Providers, f.Models)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	factory := modelregistry.NewFactory()
	return &deps{models: reg, llms: factory, judges: factory}
}

func TestRoutingCandidatesTakeOnlyDescribedModels(t *testing.T) {
	got := routingCandidates(soFile(t).Models.LLM)
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want the two described models", got)
	}
	if got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("candidates = %+v, want alpha and beta in declaration order", got)
	}
	if got[0].Description != "Frontier. Refactors." {
		t.Fatalf("description = %q, want it verbatim", got[0].Description)
	}
}

func TestSystemOneOptionUnsetIsNoOption(t *testing.T) {
	opt, err := systemOneOption(&cliConfig{deps: soDeps(t)})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if opt != nil {
		t.Fatal("no decision model named means no option")
	}
}

func TestSystemOneOptionRejectsABadRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
	}{
		{"undeclared alias", "ghost"},
		{"a chat model", "alpha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := systemOneOption(&cliConfig{SystemOneModel: tt.ref, deps: soDeps(t)})
			if err == nil {
				t.Fatal("want an error naming the flag")
			}
			if !strings.Contains(err.Error(), "--systemone-model") {
				t.Fatalf("error = %q, want it to name the flag", err)
			}
		})
	}
}

func TestSystemOneOptionBuildsFromTheConfig(t *testing.T) {
	opt, err := systemOneOption(&cliConfig{
		SystemOneModel:    "jev",
		Model:             "alpha",
		RoutingCandidates: routingCandidates(soFile(t).Models.LLM),
		deps:              soDeps(t),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if opt == nil {
		t.Fatal("a named decision model must produce an option")
	}
}

// The block's pointers reach the feature config, and the two defaults this
// layer owns are applied: the message tail, and the advisor consumer being
// off when no advisor tool is mounted.
func TestSystemOneOptionAppliesTheBlock(t *testing.T) {
	const withBlock = soRegistryYAML +
		"systemone_model: jev\n" +
		"systemone:\n" +
		"  recent_messages: 0\n" +
		"  gate:\n    enabled: false\n    secrets_ask: 0.42\n" +
		"  routing:\n    enabled: false\n"

	var f cfgfile.File
	if err := yaml.Unmarshal([]byte(withBlock), &f); err != nil {
		t.Fatal(err)
	}
	cfg := &cliConfig{deps: soDeps(t)}
	mergeConfigFile(cfg, f, func(string) bool { return false }, func(string) bool { return false })

	if cfg.SystemOneModel != "jev" {
		t.Fatalf("systemone_model = %q", cfg.SystemOneModel)
	}
	if cfg.SystemOne == nil || cfg.SystemOne.RecentMessages == nil || *cfg.SystemOne.RecentMessages != 0 {
		t.Fatalf("block not merged: %+v", cfg.SystemOne)
	}
	if len(cfg.RoutingCandidates) != 2 {
		t.Fatalf("candidates = %+v, want both described models", cfg.RoutingCandidates)
	}
	if _, err := systemOneOption(cfg); err != nil {
		t.Fatalf("systemOneOption: %v", err)
	}
}

func TestSystemOneSummary(t *testing.T) {
	tests := []struct {
		name  string
		mutit func(*cfgfile.File)
		want  string
	}{
		{"no decision model prints nothing", func(f *cfgfile.File) {}, ""},
		{"two described models", func(f *cfgfile.File) { f.SystemOneModel = "jev" }, "routing candidates: alpha, beta"},
		{"one described model explains itself", func(f *cfgfile.File) {
			f.SystemOneModel = "jev"
			f.Models.LLM[1].Description = ""
		}, "routing needs two"},
		{"none described explains itself", func(f *cfgfile.File) {
			f.SystemOneModel = "jev"
			f.Models.LLM[0].Description = ""
			f.Models.LLM[1].Description = ""
		}, "none (add a description:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := soFile(t)
			tt.mutit(&f)
			got := systemOneSummary(f)
			if tt.want == "" {
				if got != "" {
					t.Fatalf("got %q, want nothing", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("summary = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// End to end through the real command: the file's keys reach --list-models.
func TestRootCmdListModelsShowsTheDecisionModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenzing.yaml")
	content := soRegistryYAML + "model: alpha\nsystemone_model: jev\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectUserConfig(t)

	out := &strings.Builder{}
	cmd := newRootCmd()
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--list-models", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"systemone_model: jev", "routing candidates: alpha, beta"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--list-models missing %q:\n%s", want, out.String())
		}
	}
}
