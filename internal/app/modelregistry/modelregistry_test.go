package modelregistry

import (
	"math"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
)

// testProviders is the provider set the registry tests build against: two
// instances of one type, so the tests can prove a model binds to the
// instance it names rather than to the type.
var testProviders = []config.Provider{
	{Name: "local", Type: "ollama", URL: "http://localhost:11434"},
	{Name: "cloud", Type: "ollama", URL: "https://ollama.com/", APIKey: "sk-cloud"},
	{Name: "claude", Type: "anthropic", URL: "https://api.anthropic.com"},
}

// testRegistry builds a registry over testProviders plus the given entries,
// failing the test on error.
func testRegistry(t *testing.T, entries ...config.ModelEntry) *Registry {
	t.Helper()
	reg, err := Build(testProviders, entries)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return reg
}

func TestBuildRegistry(t *testing.T) {
	tests := []struct {
		name    string
		entries []config.ModelEntry
		wantErr string
		check   func(t *testing.T, reg *Registry)
	}{
		{
			name: "no entries is an empty registry",
			check: func(t *testing.T, reg *Registry) {
				if len(reg.byName) != 0 {
					t.Errorf("registry not empty: %+v", reg.byName)
				}
				if len(reg.providers) != len(testProviders) {
					t.Errorf("providers not indexed: %+v", reg.providers)
				}
			},
		},
		{
			name:    "size defaults applied",
			entries: []config.ModelEntry{{Name: "small", Provider: "local", ModelName: "qwen3"}},
			check: func(t *testing.T, reg *Registry) {
				rm, err := reg.Resolve("small")
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				if rm.Def.ContextWindowSize != defaultContextWindow || rm.Def.MaxTokens != defaultMaxResponseTokens {
					t.Errorf("defaults not applied: %+v", rm.Def)
				}
			},
		},
		{
			name:    "alias and wire name are kept apart",
			entries: []config.ModelEntry{{Name: "main-model", Provider: "cloud", ModelName: "glm-5.3-flash"}},
			check: func(t *testing.T, reg *Registry) {
				rm, err := reg.Resolve("main-model")
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				// The definition carries the wire id: that is what the
				// protocol clients send and what comes back on the event.
				if rm.Def.Name != "glm-5.3-flash" {
					t.Errorf("def.Name = %q, want the wire model_name", rm.Def.Name)
				}
				if _, err := reg.Resolve("glm-5.3-flash"); err == nil {
					t.Error("wire name should not resolve; only the alias does")
				}
			},
		},
		{
			name: "model binds to the provider instance it names",
			entries: []config.ModelEntry{
				{Name: "here", Provider: "local", ModelName: "glm-5.3"},
				{Name: "there", Provider: "cloud", ModelName: "glm-5.3"},
			},
			check: func(t *testing.T, reg *Registry) {
				here, _ := reg.Resolve("here")
				there, _ := reg.Resolve("there")
				if here.Provider.URL != "http://localhost:11434" || there.Provider.URL != "https://ollama.com/" {
					t.Errorf("providers crossed: here=%q there=%q", here.Provider.URL, there.Provider.URL)
				}
				if there.Provider.APIKey != "sk-cloud" {
					t.Errorf("api key not carried: %q", there.Provider.APIKey)
				}
				// Both definitions get the provider's type, not its name.
				if here.Def.Provider != "ollama" || there.Def.Provider != "ollama" {
					t.Errorf("def.Provider should be the type: %q / %q", here.Def.Provider, there.Def.Provider)
				}
			},
		},
		{
			name: "explicit sizes, vision and reasoning effort honored",
			entries: []config.ModelEntry{{
				Name: "big", Provider: "local", ModelName: "glm-5.3",
				ContextWindow: 200000, MaxResponseTokens: 4096, Vision: true, ReasoningEffort: "max",
			}},
			check: func(t *testing.T, reg *Registry) {
				rm, _ := reg.Resolve("big")
				if rm.Def.ContextWindowSize != 200000 || rm.Def.MaxTokens != 4096 {
					t.Errorf("sizes not honored: %+v", rm.Def)
				}
				if !rm.Def.SupportsVision || rm.Def.ReasoningEffort != "max" {
					t.Errorf("flags not carried: %+v", rm.Def)
				}
			},
		},
		{
			// Pricing is matched against LLMResponseEvent.Model, which the
			// provider fills in from the response — the wire name, never
			// the local alias.
			name: "pricing is keyed by the wire model name",
			entries: []config.ModelEntry{{
				Name: "priced", Provider: "claude", ModelName: "Claude-Opus-4-6",
				Cost: &config.CostEntry{Input: 3.0, Output: 15.0},
			}},
			check: func(t *testing.T, reg *Registry) {
				p, ok := reg.pricing["claude-opus-4-6"]
				if !ok {
					t.Fatalf("pricing not keyed by wire name: %+v", reg.pricing)
				}
				if _, ok := reg.pricing["priced"]; ok {
					t.Error("pricing should not be keyed by the alias")
				}
				if math.Abs(p.CacheRead-0.3) > 1e-9 || math.Abs(p.CacheWrite-3.75) > 1e-9 {
					t.Errorf("cache rate defaults = %+v, want read 0.3 write 3.75", p)
				}
			},
		},
		{
			name: "explicit cost cache rates honored",
			entries: []config.ModelEntry{{
				Name: "priced", Provider: "claude", ModelName: "opus",
				Cost: &config.CostEntry{Input: 3.0, Output: 15.0, CacheRead: 0.5, CacheWrite: 4.0},
			}},
			check: func(t *testing.T, reg *Registry) {
				p := reg.pricing["opus"]
				if p.CacheRead != 0.5 || p.CacheWrite != 4.0 {
					t.Errorf("explicit cache rates = %+v", p)
				}
			},
		},
		{
			name:    "undeclared provider fails with the declared list",
			entries: []config.ModelEntry{{Name: "x", Provider: "nonsense", ModelName: "m"}},
			wantErr: "not declared in providers",
		},
		{
			name:    "missing model_name fails",
			entries: []config.ModelEntry{{Name: "x", Provider: "local"}},
			wantErr: "model_name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := Build(testProviders, tt.entries)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			tt.check(t, reg)
		})
	}
}

func TestResolveRefs(t *testing.T) {
	reg := testRegistry(t, config.ModelEntry{Name: "main", Provider: "cloud", ModelName: "glm-5.3"})

	tests := []struct {
		ref     string
		wantErr string
	}{
		{ref: "main"},
		{ref: "not-declared", wantErr: "not declared in models"},
		{ref: "", wantErr: "not declared in models"},
		{ref: `{"provider":"local","model_name":"qwen3"}`},
		{ref: `{"provider":"bogus","model_name":"x"}`, wantErr: "not declared in providers"},
		{ref: `{"provider":"local"}`, wantErr: "model_name is required"},
		{ref: `{not json`, wantErr: "inline model definition"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			_, err := reg.Resolve(tt.ref)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Resolve(%q): %v", tt.ref, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Resolve(%q) err = %v, want containing %q", tt.ref, err, tt.wantErr)
			}
		})
	}
}

// TestResolveInlineJSON checks explicit fields carry through, omitted ones
// get the defaults, and the named provider is attached — same as a models:
// entry.
func TestResolveInlineJSON(t *testing.T) {
	reg := testRegistry(t)

	full := `{"provider":"cloud","model_name":"glm-5.3","context_window":1048576,"max_response_tokens":65536,"vision":true}`
	rm, err := reg.Resolve(full)
	if err != nil {
		t.Fatalf("Resolve full: %v", err)
	}
	if rm.Def.Name != "glm-5.3" || rm.Provider.Name != "cloud" || rm.Provider.APIKey != "sk-cloud" {
		t.Errorf("provider not attached: def=%+v provider=%+v", rm.Def, rm.Provider)
	}
	if rm.Def.ContextWindowSize != 1048576 || rm.Def.MaxTokens != 65536 || !rm.Def.SupportsVision {
		t.Errorf("fields not carried: %+v", rm.Def)
	}

	rm, err = reg.Resolve(`{"provider":"local","model_name":"tiny"}`)
	if err != nil {
		t.Fatalf("Resolve minimal: %v", err)
	}
	if rm.Def.ContextWindowSize != defaultContextWindow || rm.Def.MaxTokens != defaultMaxResponseTokens {
		t.Errorf("defaults not applied: %+v", rm.Def)
	}
}

func TestNames(t *testing.T) {
	reg := testRegistry(t,
		config.ModelEntry{Name: "zeta", Provider: "local", ModelName: "z"},
		config.ModelEntry{Name: "alpha", Provider: "cloud", ModelName: "a"},
	)
	got := reg.Names()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("Names() = %v, want [alpha zeta]", got)
	}
}

// TestListProvesTheTableNamesEveryDeclaredModel: aliases, provider names and
// wire names all appear, sorted.
func TestList(t *testing.T) {
	reg := testRegistry(t,
		config.ModelEntry{Name: "zeta", Provider: "local", ModelName: "z"},
		config.ModelEntry{Name: "alpha", Provider: "cloud", ModelName: "a"},
	)

	out := reg.List()
	for _, want := range []string{"alpha", "zeta", "cloud", "local"} {
		if !strings.Contains(out, want) {
			t.Errorf("List missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] < lines[i-1] {
			t.Errorf("List not sorted:\n%s", out)
			break
		}
	}
}

// TestPricingReturnsACopy proves callers can scribble on the returned map
// without touching the registry's table.
func TestPricingReturnsACopy(t *testing.T) {
	reg := testRegistry(t, config.ModelEntry{
		Name: "priced", Provider: "claude", ModelName: "opus",
		Cost: &config.CostEntry{Input: 3.0, Output: 15.0},
	})

	p := reg.Pricing()
	delete(p, "opus")
	if _, ok := reg.pricing["opus"]; !ok {
		t.Error("Pricing handed out the internal map; a caller delete mutated it")
	}
}