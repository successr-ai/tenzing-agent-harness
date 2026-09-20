package modelregistry

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	protoanthropic "github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/anthropic"
	protoollama "github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ollama"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/openai_compat"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/systemone"
)

// maxCompletionTokensKey is the one Extra key that is not a request field.
// The OpenAI-compatible client renames the token-limit parameter rather
// than adding one, so it maps to a client option; current OpenAI models
// reject the un-renamed max_tokens.
const maxCompletionTokensKey = "max_completion_tokens"

// buildLLM constructs a protocol client for a resolved model. Endpoint and
// key come from the model's provider entry and nowhere else: tenzing.yaml
// declares both, so there is no env-var fallback. A provider's URL may be
// empty only for anthropic and ollama, whose clients carry their vendor's
// endpoint; the client options treat empty as "keep the default".
func buildLLM(rm ResolvedModel) (common.LLM, error) {
	def, prov := rm.Def, rm.Provider
	key, url := prov.APIKey, prov.URL

	switch prov.Type {
	case "anthropic":
		if def.ReasoningEffort != "" {
			slog.Warn("reasoning_effort ignored: provider takes a numeric thinking budget, not tiers",
				"model", def.Name, "provider", prov.Name, "reasoning_effort", def.ReasoningEffort)
		}
		return protoanthropic.NewClient(def,
			protoanthropic.WithAPIKey(key),
			protoanthropic.WithBaseURL(url))
	case "ollama":
		opts := []protoollama.ClientOption{
			protoollama.WithAPIKey(key),
			protoollama.WithBaseURL(url),
		}
		if def.ReasoningEffort != "" {
			opts = append(opts, protoollama.WithReasoningEffort(def.ReasoningEffort))
		}
		return protoollama.NewClient(def, opts...)
	case config.DefaultProviderType:
		return openai_compat.NewClient(def, compatOptions(def, prov)...)
	case config.SystemOneProviderType:
		// Reachable only through an inline --model ref: config validation
		// keeps a systemone provider out of models.llm:.
		return nil, fmt.Errorf("%s is served by a %s provider (%s): it is a System One model and answers typed questions, not chat turns",
			def.Name, config.SystemOneProviderType, prov.Name)
	default:
		return nil, fmt.Errorf("build LLM for %s: %w", def.Name, common.ErrUnknownProvider)
	}
}

// compatOptions builds the openai_compat client's option list: the provider's
// own name (not its type — several compat backends can be declared at once,
// and "groq" is more use in a log line than "openai_compat" repeated),
// endpoint and key, the reasoning effort when set, and the provider's
// extra: map turned into verbatim request-field options.
func compatOptions(def common.ModelDefinition, prov config.Provider) []openai_compat.ClientOption {
	opts := []openai_compat.ClientOption{
		openai_compat.WithName(prov.Name),
		openai_compat.WithAPIKey(prov.APIKey),
		openai_compat.WithBaseURL(prov.URL),
	}
	if def.ReasoningEffort != "" {
		opts = append(opts, openai_compat.WithReasoningEffort(def.ReasoningEffort))
	}
	return append(opts, extraOptions(prov.Extra)...)
}

// extraOptions turns a provider's extra: map into client options: the
// reserved max_completion_tokens key becomes the rename option, everything
// else is injected verbatim into each request body by dotted path. Keys are
// applied in sorted order — map iteration is random and the client keeps
// extras as an ordered slice, so unsorted iteration would make two
// identical configs build subtly different clients.
func extraOptions(extra map[string]any) []openai_compat.ClientOption {
	var opts []openai_compat.ClientOption
	for _, k := range slices.Sorted(maps.Keys(extra)) {
		v := extra[k]
		if k == maxCompletionTokensKey {
			if on, _ := v.(bool); on {
				opts = append(opts, openai_compat.WithMaxCompletionTokens())
			}
			continue
		}
		opts = append(opts, openai_compat.WithExtraField(k, v))
	}
	return opts
}

// buildSystemOne constructs a System One client for a resolved decision
// model. Endpoint and key come from the provider entry, as for every other
// protocol; an empty URL keeps the client's own default (TypeSafe's).
func buildSystemOne(rs ResolvedSystemOne) (common.SystemOne, error) {
	if rs.Provider.Type != config.SystemOneProviderType {
		return nil, fmt.Errorf("provider %s has type %q, not %s", rs.Provider.Name, rs.Provider.Type, config.SystemOneProviderType)
	}
	return systemone.NewClient(
		systemone.WithAPIKey(rs.Provider.APIKey),
		systemone.WithBaseURL(rs.Provider.URL),
		systemone.WithModel(rs.Name))
}

// Factory builds clients on demand and reuses one per distinct
// provider/model/reasoning-effort, so model switch-back is free and roles
// sharing a model share a client (effort is in the key because inline model
// refs can name the same model at different tiers). Construct with
// NewFactory and inject it; there is no process-wide instance. LLM and
// System One clients live in separate caches because they are separate
// interfaces, not two flavours of one.
type Factory struct {
	mu         sync.Mutex
	clients    map[string]common.LLM
	systemOnes map[string]common.SystemOne
}

// NewFactory returns an empty client cache.
func NewFactory() *Factory {
	return &Factory{
		clients:    make(map[string]common.LLM),
		systemOnes: make(map[string]common.SystemOne),
	}
}

// Get returns the cached client for the resolved model, building it on
// first use.
func (f *Factory) Get(rm ResolvedModel) (common.LLM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cacheKey := fmt.Sprintf("%s|%s|%s", rm.Provider.Name, rm.Def.Name, rm.Def.ReasoningEffort)
	if llm, ok := f.clients[cacheKey]; ok {
		return llm, nil
	}
	llm, err := buildLLM(rm)
	if err != nil {
		return nil, fmt.Errorf("build LLM for %s: %w", rm.Def.Name, err)
	}
	f.clients[cacheKey] = llm
	return llm, nil
}

// GetSystemOne returns the cached System One client for the resolved model,
// building it on first use.
func (f *Factory) GetSystemOne(rs ResolvedSystemOne) (common.SystemOne, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cacheKey := fmt.Sprintf("%s|%s", rs.Provider.Name, rs.Name)
	if client, ok := f.systemOnes[cacheKey]; ok {
		return client, nil
	}
	client, err := buildSystemOne(rs)
	if err != nil {
		return nil, fmt.Errorf("build System One client for %s: %w", rs.Name, err)
	}
	f.systemOnes[cacheKey] = client
	return client, nil
}
