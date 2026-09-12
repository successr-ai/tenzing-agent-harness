package modelregistry

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/openai_compat"
)

// TestBuildLLMUsesProviderEndpoint proves the client is built from the
// model's own provider entry — two models of the same type behind different
// endpoints must not collapse onto one client.
func TestBuildLLMUsesProviderEndpoint(t *testing.T) {
	reg := testRegistry(t,
		config.ModelEntry{Name: "here", Provider: "local", ModelName: "glm-5.3"},
		config.ModelEntry{Name: "there", Provider: "cloud", ModelName: "glm-5.3"},
	)

	here, _ := reg.Resolve("here")
	there, _ := reg.Resolve("there")
	for _, rm := range []ResolvedModel{here, there} {
		if _, err := buildLLM(rm); err != nil {
			t.Fatalf("buildLLM(%s): %v", rm.Provider.Name, err)
		}
	}
}

// TestFactoryGetCoversEveryExportedBranchOfTheCache: miss builds, hit
// reuses, and clients for different providers never share.
func TestFactoryGet(t *testing.T) {
	reg := testRegistry(t,
		config.ModelEntry{Name: "here", Provider: "local", ModelName: "glm-5.3"},
		config.ModelEntry{Name: "there", Provider: "cloud", ModelName: "glm-5.3"},
	)
	f := NewFactory()

	here, _ := reg.Resolve("here")
	there, _ := reg.Resolve("there")

	a, err := f.Get(here)
	if err != nil {
		t.Fatalf("Get(here): %v", err)
	}
	b, err := f.Get(there)
	if err != nil {
		t.Fatalf("Get(there): %v", err)
	}
	if a == b {
		t.Error("clients for different providers were shared")
	}

	again, err := f.Get(here)
	if err != nil {
		t.Fatalf("Get(here) again: %v", err)
	}
	if again != a {
		t.Error("same model rebuilt instead of reusing the cached client")
	}
}

// TestBuildLLMUnknownType covers the switch's default arm; config
// validation rejects unknown types first, so this is the backstop.
func TestBuildLLMUnknownType(t *testing.T) {
	rm := ResolvedModel{Provider: config.Provider{Name: "p", Type: "llamafile", URL: "http://x"}}
	if _, err := buildLLM(rm); err == nil {
		t.Fatal("unknown provider type should fail")
	}
}

// clientOf builds the LLM for a one-off provider/model pair and returns the
// concrete openai_compat client, so the tests can read back what the
// provider's extra: map turned into.
func clientOf(t *testing.T, prov config.Provider) *openai_compat.Client {
	t.Helper()
	llm, err := buildLLM(ResolvedModel{
		Def:      common.ModelDefinition{Name: "m", Provider: prov.Type},
		Provider: prov,
	})
	if err != nil {
		t.Fatalf("buildLLM: %v", err)
	}
	c, ok := llm.(*openai_compat.Client)
	if !ok {
		t.Fatalf("got %T, want *openai_compat.Client", llm)
	}
	return c
}

// TestCompatClientNamedAfterProvider proves the client reports the
// provider's own name, not its type — several openai_compat backends can be
// declared at once, and they would otherwise be indistinguishable in logs.
func TestCompatClientNamedAfterProvider(t *testing.T) {
	c := clientOf(t, config.Provider{Name: "groq", Type: config.DefaultProviderType, URL: "http://x"})
	if c.Name != "groq" {
		t.Errorf("client name = %q, want the provider name %q", c.Name, "groq")
	}
}

// TestExtraMaxCompletionTokens covers the one reserved extra: key. It is a
// parameter rename, not a request field, so it has to reach the client as
// an option.
func TestExtraMaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		{"absent", nil, false},
		{"true", map[string]any{"max_completion_tokens": true}, true},
		{"false", map[string]any{"max_completion_tokens": false}, false},
		{"non-bool ignored", map[string]any{"max_completion_tokens": "yes"}, false},
		{"alongside request fields", map[string]any{
			"max_completion_tokens": true,
			"provider.sort":         "throughput",
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := clientOf(t, config.Provider{
				Name: "p", Type: config.DefaultProviderType, URL: "http://x", Extra: tt.extra,
			})
			if c.UseMaxCompletionTokens != tt.want {
				t.Errorf("UseMaxCompletionTokens = %v, want %v", c.UseMaxCompletionTokens, tt.want)
			}
		})
	}
}

// TestExtraIgnoredOnOtherTypes proves extra: on anthropic/ollama is dropped
// rather than failing: it is an openai_compat concept, and those clients
// have no equivalent.
func TestExtraIgnoredOnOtherTypes(t *testing.T) {
	for _, typ := range []string{"anthropic", "ollama"} {
		t.Run(typ, func(t *testing.T) {
			_, err := buildLLM(ResolvedModel{
				Def: common.ModelDefinition{Name: "m", Provider: typ},
				Provider: config.Provider{
					Name: "p", Type: typ,
					Extra: map[string]any{"provider.sort": "throughput", "max_completion_tokens": true},
				},
			})
			if err != nil {
				t.Errorf("extra on %s should be ignored, got: %v", typ, err)
			}
		})
	}
}

// TestBuildLLMEmptyURL proves anthropic and ollama build with no URL — their
// clients supply the vendor endpoint, which is why config only requires a
// url for openai_compat.
func TestBuildLLMEmptyURL(t *testing.T) {
	for _, typ := range []string{"anthropic", "ollama"} {
		t.Run(typ, func(t *testing.T) {
			_, err := buildLLM(ResolvedModel{
				Def:      common.ModelDefinition{Name: "m", Provider: typ},
				Provider: config.Provider{Name: "p", Type: typ},
			})
			if err != nil {
				t.Errorf("buildLLM(%s) with no url: %v", typ, err)
			}
		})
	}
}