package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	protoanthropic "github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/anthropic"
	protoollama "github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ollama"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/openai_compat"
)

// Default OpenAI-compatible endpoints for providers without a native
// protocol package. Overridable per provider via models.yaml base_url.
const (
	cerebrasBaseURL   = "https://api.cerebras.ai/v1"
	lightningBaseURL  = "https://lightning.ai/api/v1"
	openRouterBaseURL = "https://openrouter.ai/api/v1"
)

// providerEnvKeys maps each provider to the conventional env var holding
// its API key. A missing key is not an error here — providers that require
// auth fail on the first request instead (and local Ollama needs none).
var providerEnvKeys = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"cerebras":   "CEREBRAS_API_KEY",
	"lightning":  "LIGHTNING_API_KEY",
	"ollama":     "OLLAMA_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

// buildLLM constructs a protocol client for the model definition, resolving
// the API key from the provider's conventional env var. baseURL overrides
// the provider default endpoint when non-empty (models.yaml base_url).
func buildLLM(def common.ModelDefinition, baseURL string) (common.LLM, error) {
	key := os.Getenv(providerEnvKeys[def.Provider])

	compat := func(name string, defaultURL string, extra ...openai_compat.ClientOption) (common.LLM, error) {
		url := baseURL
		if url == "" {
			url = defaultURL
		}
		opts := append([]openai_compat.ClientOption{
			openai_compat.WithName(name),
			openai_compat.WithAPIKey(key),
			openai_compat.WithBaseURL(url),
		}, extra...)
		return openai_compat.NewClient(def, opts...)
	}

	switch def.Provider {
	case "anthropic":
		return protoanthropic.NewClient(def, protoanthropic.WithAPIKey(key))
	case "ollama":
		opts := []protoollama.ClientOption{protoollama.WithAPIKey(key)}
		if baseURL != "" {
			opts = append(opts, protoollama.WithBaseURL(baseURL))
		}
		return protoollama.NewClient(def, opts...)
	case "openai":
		return compat("openai", "", openai_compat.WithMaxCompletionTokens())
	case "cerebras":
		return compat("cerebras", cerebrasBaseURL)
	case "lightning":
		return compat("lightning", lightningBaseURL)
	case "openrouter":
		return compat("openrouter", openRouterBaseURL)
	default:
		return nil, fmt.Errorf("build LLM for %s: %w", def.Name, common.ErrUnknownProvider)
	}
}

// llmCache builds LLM clients on demand via buildLLM and reuses one client
// per distinct provider/model/baseURL, so model switch-back is free and
// roles sharing a model share a client. Base URLs come from the process-wide
// model registry (models.yaml base_url entries).
type llmCache struct {
	mu      sync.Mutex
	clients map[string]common.LLM
}

// llms is the process-wide client cache, alongside the models registry.
var llms = &llmCache{clients: make(map[string]common.LLM)}

func (c *llmCache) get(def common.ModelDefinition) (common.LLM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	url := models.baseURLs[def.Provider]
	cacheKey := fmt.Sprintf("%s|%s|%s", def.Provider, def.Name, url)
	if llm, ok := c.clients[cacheKey]; ok {
		return llm, nil
	}
	llm, err := buildLLM(def, url)
	if err != nil {
		return nil, fmt.Errorf("build LLM for %s: %w", def.Name, err)
	}
	c.clients[cacheKey] = llm
	return llm, nil
}
