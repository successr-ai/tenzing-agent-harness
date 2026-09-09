package main

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
func testRegistry(t *testing.T, entries ...config.ModelEntry) *modelRegistry {
	t.Helper()
	reg, err := buildRegistry(testProviders, entries)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	return reg
}

// seedRegistry points the process-wide registry at one declared model for
// the duration of the test and returns its alias. Tests that go through
// resolveModel (print mode, the root command) need a registry: nothing is
// compiled in any more.
func seedRegistry(t *testing.T) string {
	t.Helper()
	orig := models
	t.Cleanup(func() { models = orig })
	models = testRegistry(t, config.ModelEntry{Name: "test-model", Provider: "local", ModelName: "glm-5.3"})
	return "test-model"
}

func TestBuildRegistry(t *testing.T) {
	tests := []struct {
		name    string
		entries []config.ModelEntry
		wantErr string
		check   func(t *testing.T, reg *modelRegistry)
	}{
		{
			name: "no entries is an empty registry",
			check: func(t *testing.T, reg *modelRegistry) {
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
			check: func(t *testing.T, reg *modelRegistry) {
				rm, err := reg.resolve("small")
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				if rm.def.ContextWindowSize != defaultContextWindow || rm.def.MaxTokens != defaultMaxResponseTokens {
					t.Errorf("defaults not applied: %+v", rm.def)
				}
			},
		},
		{
			name:    "alias and wire name are kept apart",
			entries: []config.ModelEntry{{Name: "main-model", Provider: "cloud", ModelName: "glm-5.3-flash"}},
			check: func(t *testing.T, reg *modelRegistry) {
				rm, err := reg.resolve("main-model")
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				// The definition carries the wire id: that is what the
				// protocol clients send and what comes back on the event.
				if rm.def.Name != "glm-5.3-flash" {
					t.Errorf("def.Name = %q, want the wire model_name", rm.def.Name)
				}
				if _, err := reg.resolve("glm-5.3-flash"); err == nil {
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
			check: func(t *testing.T, reg *modelRegistry) {
				here, _ := reg.resolve("here")
				there, _ := reg.resolve("there")
				if here.provider.URL != "http://localhost:11434" || there.provider.URL != "https://ollama.com/" {
					t.Errorf("providers crossed: here=%q there=%q", here.provider.URL, there.provider.URL)
				}
				if there.provider.APIKey != "sk-cloud" {
					t.Errorf("api key not carried: %q", there.provider.APIKey)
				}
				// Both definitions get the provider's type, not its name.
				if here.def.Provider != "ollama" || there.def.Provider != "ollama" {
					t.Errorf("def.Provider should be the type: %q / %q", here.def.Provider, there.def.Provider)
				}
			},
		},
		{
			name: "explicit sizes, vision and reasoning effort honored",
			entries: []config.ModelEntry{{
				Name: "big", Provider: "local", ModelName: "glm-5.3",
				ContextWindow: 200000, MaxResponseTokens: 4096, Vision: true, ReasoningEffort: "max",
			}},
			check: func(t *testing.T, reg *modelRegistry) {
				rm, _ := reg.resolve("big")
				if rm.def.ContextWindowSize != 200000 || rm.def.MaxTokens != 4096 {
					t.Errorf("sizes not honored: %+v", rm.def)
				}
				if !rm.def.SupportsVision || rm.def.ReasoningEffort != "max" {
					t.Errorf("flags not carried: %+v", rm.def)
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
			check: func(t *testing.T, reg *modelRegistry) {
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
			check: func(t *testing.T, reg *modelRegistry) {
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
			reg, err := buildRegistry(testProviders, tt.entries)
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

// TestResolveInlineJSON checks explicit fields carry through, omitted ones
// get the defaults, and the named provider is attached — same as a models:
// entry.
func TestResolveInlineJSON(t *testing.T) {
	reg := testRegistry(t)

	full := `{"provider":"cloud","model_name":"glm-5.3","context_window":1048576,"max_response_tokens":65536,"vision":true}`
	rm, err := reg.resolve(full)
	if err != nil {
		t.Fatalf("resolve full: %v", err)
	}
	if rm.def.Name != "glm-5.3" || rm.provider.Name != "cloud" || rm.provider.APIKey != "sk-cloud" {
		t.Errorf("provider not attached: def=%+v provider=%+v", rm.def, rm.provider)
	}
	if rm.def.ContextWindowSize != 1048576 || rm.def.MaxTokens != 65536 || !rm.def.SupportsVision {
		t.Errorf("fields not carried: %+v", rm.def)
	}

	rm, err = reg.resolve(`{"provider":"local","model_name":"tiny"}`)
	if err != nil {
		t.Fatalf("resolve minimal: %v", err)
	}
	if rm.def.ContextWindowSize != defaultContextWindow || rm.def.MaxTokens != defaultMaxResponseTokens {
		t.Errorf("defaults not applied: %+v", rm.def)
	}
}

// TestResolveModelErrorListsDeclared proves a bad alias reports what is
// declared, while a bad inline ref does not (the list wouldn't help).
func TestResolveModelErrorListsDeclared(t *testing.T) {
	orig := models
	t.Cleanup(func() { models = orig })
	models = testRegistry(t, config.ModelEntry{Name: "main-model", Provider: "cloud", ModelName: "glm-5.3"})

	if _, err := resolveModel("main-model"); err != nil {
		t.Fatalf("declared model should resolve: %v", err)
	}

	_, err := resolveModel("does-not-exist")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "main-model") {
		t.Errorf("error should list declared models, got: %v", err)
	}

	_, err = resolveModel(`{"provider":"local"}`)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "main-model") {
		t.Errorf("inline error should not list models, got: %v", err)
	}
}

func TestModelList(t *testing.T) {
	orig := models
	t.Cleanup(func() { models = orig })
	models = testRegistry(t,
		config.ModelEntry{Name: "zeta", Provider: "local", ModelName: "z"},
		config.ModelEntry{Name: "alpha", Provider: "cloud", ModelName: "a"},
	)

	out := modelList()
	for _, want := range []string{"alpha", "zeta", "cloud", "local"} {
		if !strings.Contains(out, want) {
			t.Errorf("modelList missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !sortedStrings(lines) {
		t.Errorf("modelList not sorted:\n%s", out)
	}
}

func TestMergeProviderFlags(t *testing.T) {
	file := []config.Provider{
		{Name: "local", Type: "ollama", URL: "http://localhost:11434"},
		{Name: "claude", Type: "anthropic", URL: "https://api.anthropic.com"},
	}

	t.Run("no flags leaves the file list alone", func(t *testing.T) {
		got, err := mergeProviderFlags(file, nil)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d providers, want 2", len(got))
		}
	})

	t.Run("matching name replaces in place", func(t *testing.T) {
		got, err := mergeProviderFlags(file,
			[]string{`{"name":"local","type":"ollama","url":"http://box:11434","api_key":"sk-x"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d providers, want 2 (replace, not append)", len(got))
		}
		if got[0].Name != "local" || got[0].URL != "http://box:11434" || got[0].APIKey != "sk-x" {
			t.Errorf("not replaced in place: %+v", got[0])
		}
		// The file list is the caller's; merging must not scribble on it.
		if file[0].URL != "http://localhost:11434" {
			t.Errorf("file list mutated: %+v", file[0])
		}
	})

	t.Run("omitted type defaults to openai_compat", func(t *testing.T) {
		got, err := mergeProviderFlags(file,
			[]string{`{"name":"groq","url":"https://api.groq.com/openai/v1"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got[2].Type != config.DefaultProviderType {
			t.Errorf("flag provider type = %q, want the default", got[2].Type)
		}
	})

	t.Run("new name is appended", func(t *testing.T) {
		got, err := mergeProviderFlags(file,
			[]string{`{"name":"cloud","type":"ollama","url":"https://ollama.com/"}`})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(got) != 3 || got[2].Name != "cloud" {
			t.Errorf("not appended: %+v", got)
		}
	})

	t.Run("invalid flags rejected", func(t *testing.T) {
		tests := []struct {
			name    string
			flag    string
			wantErr string
		}{
			{"malformed", `{not json`, "--provider[0]"},
			{"no name", `{"type":"ollama","url":"http://x"}`, "name is required"},
			{"unknown type", `{"name":"n","type":"llamafile","url":"http://x"}`, "unknown type"},
			{"no url on compat", `{"name":"n"}`, "url is required for openai_compat"},
			{"vendor name is not a type", `{"name":"n","type":"openrouter","url":"http://x"}`, "unknown type"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := mergeProviderFlags(file, []string{tt.flag})
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			})
		}
	})
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] {
			return false
		}
	}
	return true
}
