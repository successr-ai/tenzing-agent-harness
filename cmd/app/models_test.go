package main

import (
	"math"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	pkgmodels "github.com/successr-ai/tenzing-agent-harness/pkg/models"
)

func TestBuildRegistry(t *testing.T) {
	tests := []struct {
		name    string
		section config.ModelsSection
		wantErr string
		check   func(t *testing.T, reg *modelRegistry)
	}{
		{
			name: "zero section is empty registry",
			check: func(t *testing.T, reg *modelRegistry) {
				if reg.defaultModel.Name != "" || len(reg.custom) != 0 || len(reg.baseURLs) != 0 {
					t.Errorf("registry not empty: %+v", reg)
				}
			},
		},
		{
			name: "custom model with defaults applied",
			section: config.ModelsSection{Entries: []config.ModelEntry{
				{Provider: "ollama", Name: "my-custom"},
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				def, err := reg.resolve("ollama/my-custom")
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				if def.ContextWindowSize != defaultCustomContextWindow || def.MaxTokens != defaultCustomMaxTokens {
					t.Errorf("defaults not applied: %+v", def)
				}
				if def.Provider != "ollama" {
					t.Errorf("provider = %q", def.Provider)
				}
			},
		},
		{
			name: "explicit sizes and base url",
			section: config.ModelsSection{Entries: []config.ModelEntry{
				{Provider: "ollama", Name: "big", ContextWindow: 200000, MaxTokens: 4096, BaseURL: "http://box:11434"},
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				def, _ := reg.resolve("ollama/big")
				if def.ContextWindowSize != 200000 || def.MaxTokens != 4096 {
					t.Errorf("sizes not honored: %+v", def)
				}
				if reg.baseURLs["ollama"] != "http://box:11434" {
					t.Errorf("base url = %q", reg.baseURLs["ollama"])
				}
			},
		},
		{
			name: "vision flag honored",
			section: config.ModelsSection{Entries: []config.ModelEntry{
				{Provider: "ollama", Name: "seeing", Vision: true},
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				def, _ := reg.resolve("ollama/seeing")
				if !def.SupportsVision {
					t.Errorf("vision flag not applied: %+v", def)
				}
			},
		},
		{
			name: "default referencing custom entry",
			section: config.ModelsSection{
				Default: "ollama/my-custom",
				Entries: []config.ModelEntry{{Provider: "ollama", Name: "my-custom"}},
			},
			check: func(t *testing.T, reg *modelRegistry) {
				if reg.defaultModel.Name != "my-custom" {
					t.Errorf("default = %+v", reg.defaultModel)
				}
			},
		},
		{
			name: "cost cache rates default to anthropic convention",
			section: config.ModelsSection{Entries: []config.ModelEntry{
				{Provider: "anthropic", Name: "priced", Cost: &config.CostEntry{Input: 3.0, Output: 15.0}},
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				p, ok := reg.pricing["priced"]
				if !ok {
					t.Fatal("pricing entry missing")
				}
				if math.Abs(p.CacheRead-0.3) > 1e-9 || math.Abs(p.CacheWrite-3.75) > 1e-9 {
					t.Errorf("cache rate defaults = %+v, want read 0.3 write 3.75", p)
				}
			},
		},
		{
			name: "explicit cost cache rates honored",
			section: config.ModelsSection{Entries: []config.ModelEntry{
				{Provider: "anthropic", Name: "priced", Cost: &config.CostEntry{Input: 3.0, Output: 15.0, CacheRead: 0.5, CacheWrite: 4.0}},
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				p := reg.pricing["priced"]
				if p.CacheRead != 0.5 || p.CacheWrite != 4.0 {
					t.Errorf("explicit cache rates = %+v", p)
				}
			},
		},
		{
			name:    "default referencing unknown model fails",
			section: config.ModelsSection{Default: "ollama/does-not-exist"},
			wantErr: "not found",
		},
		{
			name:    "unknown provider fails with provider list",
			section: config.ModelsSection{Entries: []config.ModelEntry{{Provider: "nonsense", Name: "x"}}},
			wantErr: "unknown provider",
		},
		{
			name:    "missing name fails",
			section: config.ModelsSection{Entries: []config.ModelEntry{{Provider: "ollama"}}},
			wantErr: "name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := buildRegistry(tt.section)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildRegistry: %v", err)
			}
			tt.check(t, reg)
		})
	}
}

func TestResolveRefs(t *testing.T) {
	reg := emptyRegistry()

	tests := []struct {
		ref     string
		wantErr string
	}{
		{ref: "anthropic/" + pkgmodels.Anthropic_ClaudeHaiku4_5.GetName()},
		{ref: "no-slash", wantErr: "provider/model-name"},
		{ref: "bogus/model", wantErr: "unknown provider"},
		{ref: "ollama/never-heard-of-it", wantErr: "not found"},
		{ref: `{"provider":"openrouter","name":"deepseek/deepseek-v4-flash-0731"}`},
		{ref: `{"provider":"bogus","name":"x"}`, wantErr: "unknown provider"},
		{ref: `{"provider":"openrouter"}`, wantErr: "name is required"},
		{ref: `{not json`, wantErr: "inline model definition"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			_, err := reg.resolve(tt.ref)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("resolve(%q): %v", tt.ref, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolve(%q) err = %v, want containing %q", tt.ref, err, tt.wantErr)
			}
		})
	}
}

// TestResolveInlineJSON checks explicit fields carry through and omitted
// ones get the custom-model defaults, same as models.yaml entries.
func TestResolveInlineJSON(t *testing.T) {
	reg := emptyRegistry()

	full := `{"provider":"openrouter","name":"deepseek/deepseek-v4-flash-0731","context_window":1048576,"max_tokens":65536,"vision":true}`
	def, err := reg.resolve(full)
	if err != nil {
		t.Fatalf("resolve full: %v", err)
	}
	if def.Provider != "openrouter" || def.Name != "deepseek/deepseek-v4-flash-0731" {
		t.Errorf("got %s/%s", def.Provider, def.Name)
	}
	if def.ContextWindowSize != 1048576 || def.MaxTokens != 65536 || !def.SupportsVision {
		t.Errorf("fields not carried: ctx=%d max=%d vision=%v", def.ContextWindowSize, def.MaxTokens, def.SupportsVision)
	}

	minimal := `{"provider":"ollama","name":"tiny"}`
	def, err = reg.resolve(minimal)
	if err != nil {
		t.Fatalf("resolve minimal: %v", err)
	}
	if def.ContextWindowSize != defaultCustomContextWindow || def.MaxTokens != defaultCustomMaxTokens {
		t.Errorf("defaults not applied: ctx=%d max=%d", def.ContextWindowSize, def.MaxTokens)
	}
}

func TestResolveModel(t *testing.T) {
	known := pkgmodels.Ollama_GLM5_2_Cloud.(common.ModelDefinition)
	knownKey := modelKey(known.Provider, known.Name)
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"known model by its own key", knownKey, false},
		{"case-insensitive", strings.ToUpper(knownKey), false},
		{"unknown model", "ollama/does-not-exist", true},
		{"unknown provider", "nope/whatever", true},
		{"malformed no slash", "justaname", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveModel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveModel(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveModel(%q) error: %v", tt.in, err)
			}
			if got.Name != known.Name || got.Provider != known.Provider {
				t.Errorf("resolveModel(%q) = %s/%s, want %s/%s", tt.in, got.Provider, got.Name, known.Provider, known.Name)
			}
		})
	}
}

func TestResolveModelErrorListsValidNames(t *testing.T) {
	_, err := resolveModel("ollama/does-not-exist")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), modelKey(pkgmodels.Ollama_GLM5_2_Cloud.(common.ModelDefinition).Provider, pkgmodels.Ollama_GLM5_2_Cloud.GetName())) {
		t.Errorf("error should list valid models, got: %v", err)
	}
}

func TestModelList(t *testing.T) {
	out := modelList()
	if !strings.Contains(out, modelKey(pkgmodels.Anthropic_ClaudeOpus4_6.(common.ModelDefinition).Provider, pkgmodels.Anthropic_ClaudeOpus4_6.GetName())) {
		t.Errorf("modelList missing anthropic entry:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !sortedStrings(lines) {
		t.Error("modelList not sorted")
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] {
			return false
		}
	}
	return true
}
