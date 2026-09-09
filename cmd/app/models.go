package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"go.yaml.in/yaml/v3"

	pkgmodels "github.com/successr-ai/tenzing-agent-harness/pkg/models"
)

// Custom model entries default to a 128k context window and 32k max output
// tokens when the file omits them.
const (
	defaultCustomContextWindow = 131072
	defaultCustomMaxTokens     = 32768
)

// modelRegistry resolves "provider/name" refs to model definitions:
// custom entries from tenzing.yaml's models: section first, then the
// compiled-in standard models from pkg/tenzing.
type modelRegistry struct {
	defaultModel common.ModelDefinition // zero when the config sets no default
	custom       map[string]common.ModelDefinition
	baseURLs     map[string]string
	// pricing is keyed by lowercase model name (matching the model field of
	// LLMResponseEvent); only entries with a cost block appear.
	pricing map[string]config.CostEntry
}

// models is the process-wide registry: builtins-only until root.go loads
// tenzing.yaml at startup. Tests exercising buildRegistry construct
// their own instances.
var models = emptyRegistry()

// emptyRegistry returns a registry with no custom entries — builtins only.
func emptyRegistry() *modelRegistry {
	return &modelRegistry{
		custom:   map[string]common.ModelDefinition{},
		baseURLs: map[string]string{},
		pricing:  map[string]config.CostEntry{},
	}
}

// availableRefs lists every resolvable "provider/name" ref: custom entries
// plus the compiled-in standard models, sorted.
func (r *modelRegistry) availableRefs() []string {
	seen := map[string]bool{}
	for k := range r.custom {
		seen[k] = true
	}
	for k := range builtinModels() {
		seen[k] = true
	}
	refs := make([]string, 0, len(seen))
	for k := range seen {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	return refs
}

var knownProviders = map[string]string{
	"anthropic":  "anthropic",
	"cerebras":   "cerebras",
	"lightning":  "lightning",
	"ollama":     "ollama",
	"openai":     "openai",
	"openrouter": "openrouter",
}

// builtinModels is the compiled-in standard model set from
// pkg/models, keyed provider/name.
func builtinModels() map[string]common.ModelDefinition {
	defs := pkgmodels.Standard()
	m := make(map[string]common.ModelDefinition, len(defs))
	for _, def := range defs {
		m[modelKey(def.Provider, def.Name)] = def
	}
	return m
}

func modelKey(p string, name string) string {
	return strings.ToLower(p) + "/" + strings.ToLower(name)
}

// buildRegistry builds the registry from tenzing.yaml's models: section.
// A zero section yields a builtins-only registry. Invalid entries are a
// startup error.
func buildRegistry(sec config.ModelsSection) (*modelRegistry, error) {
	reg := emptyRegistry()

	for i, e := range sec.Entries {
		def, err := defFromEntry(e)
		if err != nil {
			return nil, fmt.Errorf("models.entries[%d]: %w", i, err)
		}
		reg.custom[modelKey(def.Provider, e.Name)] = def
		if e.BaseURL != "" {
			reg.baseURLs[def.Provider] = e.BaseURL
		}
		if e.Cost != nil {
			cost := *e.Cost
			if cost.CacheRead == 0 {
				cost.CacheRead = cost.Input * 0.1
			}
			if cost.CacheWrite == 0 {
				cost.CacheWrite = cost.Input * 1.25
			}
			reg.pricing[strings.ToLower(e.Name)] = cost
		}
	}

	if sec.Default != "" {
		def, err := reg.resolve(sec.Default)
		if err != nil {
			return nil, fmt.Errorf("models.default: %w", err)
		}
		reg.defaultModel = def
	}
	return reg, nil
}

// defFromEntry validates a model entry (provider known, name set) and
// applies the custom-model defaults. Shared by tenzing.yaml loading and
// inline JSON refs.
func defFromEntry(e config.ModelEntry) (common.ModelDefinition, error) {
	provider, ok := knownProviders[strings.ToLower(e.Provider)]
	if !ok {
		return common.ModelDefinition{}, fmt.Errorf("unknown provider %q (known: %s)",
			e.Provider, strings.Join(providerNames(), ", "))
	}
	if e.Name == "" {
		return common.ModelDefinition{}, fmt.Errorf("name is required")
	}
	def := common.ModelDefinition{
		Name:              e.Name,
		Provider:          provider,
		ContextWindowSize: e.ContextWindow,
		MaxTokens:         e.MaxTokens,
		SupportsVision:    e.Vision,
		ReasoningEffort:   e.ReasoningEffort,
	}
	if def.ContextWindowSize == 0 {
		def.ContextWindowSize = defaultCustomContextWindow
	}
	if def.MaxTokens == 0 {
		def.MaxTokens = defaultCustomMaxTokens
	}
	return def, nil
}

// resolve maps a model ref to a definition. A ref starting with "{" is an
// inline definition — JSON (or YAML flow) with the model entry fields, e.g.
// {"provider":"openrouter","name":"x","context_window":128000} — validated
// and defaulted like a models: entry (base_url/cost fields are ignored).
// Otherwise "provider/name" is looked up: custom entries first, then the
// compiled-in standard models.
func (r *modelRegistry) resolve(ref string) (common.ModelDefinition, error) {
	if strings.HasPrefix(ref, "{") {
		var e config.ModelEntry
		if err := yaml.Unmarshal([]byte(ref), &e); err != nil {
			return common.ModelDefinition{}, fmt.Errorf("inline model definition: %w", err)
		}
		def, err := defFromEntry(e)
		if err != nil {
			return common.ModelDefinition{}, fmt.Errorf("inline model definition: %w", err)
		}
		return def, nil
	}
	provider, name, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || name == "" {
		return common.ModelDefinition{}, fmt.Errorf("model ref %q must be provider/model-name", ref)
	}
	p, known := knownProviders[strings.ToLower(provider)]
	if !known {
		return common.ModelDefinition{}, fmt.Errorf("unknown provider %q in model ref %q (known: %s)",
			provider, ref, strings.Join(providerNames(), ", "))
	}
	key := modelKey(p, name)
	if def, ok := r.custom[key]; ok {
		return def, nil
	}
	if def, ok := builtinModels()[key]; ok {
		return def, nil
	}
	return common.ModelDefinition{}, fmt.Errorf("model %q not found: not in models config and not a built-in model", ref)
}

func providerNames() []string {
	names := make([]string, 0, len(knownProviders))
	for n := range knownProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveModel maps a "provider/name" ref to a model definition via the
// process-wide registry, listing the valid refs on failure.
func resolveModel(s string) (common.ModelDefinition, error) {
	ref := strings.TrimSpace(s)
	def, err := models.resolve(ref)
	if err != nil {
		// The model list doesn't help diagnose a bad inline definition.
		if strings.HasPrefix(ref, "{") {
			return common.ModelDefinition{}, err
		}
		return common.ModelDefinition{}, fmt.Errorf("%w; valid models:\n%s", err, modelList())
	}
	return def, nil
}

// modelList returns every resolvable model (custom entries included), one
// per line, sorted.
func modelList() string {
	var b strings.Builder
	for _, ref := range models.availableRefs() {
		d, err := models.resolve(ref)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%-40s ctx=%-8d max_tokens=%d\n", ref, d.ContextWindowSize, d.MaxTokens)
	}
	return b.String()
}
