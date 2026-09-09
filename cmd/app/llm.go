package main

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
func buildLLM(rm resolvedModel) (common.LLM, error) {
	def, prov := rm.def, rm.provider
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
		// The provider's own name, not its type: several openai_compat
		// backends can be declared at once, and "groq" is more use in a log
		// line than "openai_compat" repeated.
		opts := []openai_compat.ClientOption{
			openai_compat.WithName(prov.Name),
			openai_compat.WithAPIKey(key),
			openai_compat.WithBaseURL(url),
		}
		if def.ReasoningEffort != "" {
			opts = append(opts, openai_compat.WithReasoningEffort(def.ReasoningEffort))
		}
		return openai_compat.NewClient(def, append(opts, extraOptions(prov.Extra)...)...)
	default:
		return nil, fmt.Errorf("build LLM for %s: %w", def.Name, common.ErrUnknownProvider)
	}
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

// llmCache builds LLM clients on demand via buildLLM and reuses one client
// per distinct provider/model/reasoning-effort, so model switch-back is free
// and roles sharing a model share a client (effort is in the key because
// inline model refs can name the same model at different tiers).
type llmCache struct {
	mu      sync.Mutex
	clients map[string]common.LLM
}

// llms is the process-wide client cache, alongside the models registry.
var llms = &llmCache{clients: make(map[string]common.LLM)}

func (c *llmCache) get(rm resolvedModel) (common.LLM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cacheKey := fmt.Sprintf("%s|%s|%s", rm.provider.Name, rm.def.Name, rm.def.ReasoningEffort)
	if llm, ok := c.clients[cacheKey]; ok {
		return llm, nil
	}
	llm, err := buildLLM(rm)
	if err != nil {
		return nil, fmt.Errorf("build LLM for %s: %w", rm.def.Name, err)
	}
	c.clients[cacheKey] = llm
	return llm, nil
}
