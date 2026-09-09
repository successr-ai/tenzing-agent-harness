package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"go.yaml.in/yaml/v3"
)

// Model entries default to a 128k context window and 32k max output tokens
// when the file omits them.
const (
	defaultContextWindow     = 131072
	defaultMaxResponseTokens = 32768
)

// resolvedModel pairs a model definition with the provider instance that
// serves it. The definition alone is not enough to build a client: two
// models can share a provider type but sit behind different endpoints and
// keys, so the provider travels with the model rather than being looked up
// by type later.
type resolvedModel struct {
	def      common.ModelDefinition
	provider config.Provider
}

// modelRegistry resolves model refs to definitions. Everything comes from
// tenzing.yaml: models are keyed by the `name` alias they declare, and
// there is no compiled-in fallback set.
type modelRegistry struct {
	byName    map[string]resolvedModel
	providers map[string]config.Provider
	// pricing is keyed by lowercase wire model name (matching the model
	// field of LLMResponseEvent, which providers fill in from the response,
	// not from the local alias); only entries with a cost block appear.
	pricing map[string]config.CostEntry
}

// models is the process-wide registry: empty until root.go loads
// tenzing.yaml at startup. Tests exercising buildRegistry construct their
// own instances.
var models = emptyRegistry()

// emptyRegistry returns a registry that resolves nothing.
func emptyRegistry() *modelRegistry {
	return &modelRegistry{
		byName:    map[string]resolvedModel{},
		providers: map[string]config.Provider{},
		pricing:   map[string]config.CostEntry{},
	}
}

// names lists every declared model alias, sorted.
func (r *modelRegistry) names() []string {
	out := make([]string, 0, len(r.byName))
	for k := range r.byName {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildRegistry builds the registry from tenzing.yaml's providers: and
// models: sections. config.File.validate has already checked that names are
// unique and that every model names a declared provider, so the work here
// is defaulting and indexing.
func buildRegistry(providers []config.Provider, entries []config.ModelEntry) (*modelRegistry, error) {
	reg := emptyRegistry()
	for _, p := range providers {
		reg.providers[p.Name] = p
	}

	for i, e := range entries {
		rm, err := reg.resolveEntry(e)
		if err != nil {
			return nil, fmt.Errorf("models[%d]: %w", i, err)
		}
		reg.byName[e.Name] = rm
		if e.Cost != nil {
			cost := *e.Cost
			if cost.CacheRead == 0 {
				cost.CacheRead = cost.Input * 0.1
			}
			if cost.CacheWrite == 0 {
				cost.CacheWrite = cost.Input * 1.25
			}
			reg.pricing[strings.ToLower(e.ModelName)] = cost
		}
	}
	return reg, nil
}

// resolveEntry turns a model entry into a definition bound to its provider,
// applying the context-window and max-tokens defaults. Shared by registry
// construction and inline model refs.
func (r *modelRegistry) resolveEntry(e config.ModelEntry) (resolvedModel, error) {
	if e.ModelName == "" {
		return resolvedModel{}, fmt.Errorf("model_name is required")
	}
	prov, ok := r.providers[e.Provider]
	if !ok {
		return resolvedModel{}, fmt.Errorf("provider %q is not declared in providers: (declared: %s)",
			e.Provider, strings.Join(r.providerNames(), ", "))
	}
	// Name is the wire id: it is what protocol clients send and what comes
	// back on LLMResponseEvent. The alias never leaves this process.
	//
	// This is also where the config's max_response_tokens becomes the
	// library's MaxTokens (common.ModelDefinition keeps the shorter name),
	// so grepping the config spelling stops here rather than at the
	// provider clients that consume it.
	def := common.ModelDefinition{
		Name:              e.ModelName,
		Provider:          prov.Type,
		ContextWindowSize: e.ContextWindow,
		MaxTokens:         e.MaxResponseTokens,
		SupportsVision:    e.Vision,
		ReasoningEffort:   e.ReasoningEffort,
	}
	if def.ContextWindowSize == 0 {
		def.ContextWindowSize = defaultContextWindow
	}
	if def.MaxTokens == 0 {
		def.MaxTokens = defaultMaxResponseTokens
	}
	return resolvedModel{def: def, provider: prov}, nil
}

func (r *modelRegistry) providerNames() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolve maps a model ref to a definition and its provider. A ref starting
// with "{" is an inline definition — JSON (or YAML flow) with the model
// entry fields, e.g. {"provider":"ollama-cloud","model_name":"glm-5.3"} —
// validated and defaulted like a models: entry, and naming a provider that
// must already be declared. Otherwise the ref is a model alias.
func (r *modelRegistry) resolve(ref string) (resolvedModel, error) {
	if strings.HasPrefix(ref, "{") {
		var e config.ModelEntry
		if err := yaml.Unmarshal([]byte(ref), &e); err != nil {
			return resolvedModel{}, fmt.Errorf("inline model definition: %w", err)
		}
		rm, err := r.resolveEntry(e)
		if err != nil {
			return resolvedModel{}, fmt.Errorf("inline model definition: %w", err)
		}
		return rm, nil
	}
	rm, ok := r.byName[ref]
	if !ok {
		return resolvedModel{}, fmt.Errorf("model %q is not declared in models:", ref)
	}
	return rm, nil
}

// resolveModel maps a ref to a model via the process-wide registry, listing
// the declared models on failure.
func resolveModel(s string) (resolvedModel, error) {
	ref := strings.TrimSpace(s)
	rm, err := models.resolve(ref)
	if err != nil {
		// The model list doesn't help diagnose a bad inline definition.
		if strings.HasPrefix(ref, "{") {
			return resolvedModel{}, err
		}
		return resolvedModel{}, fmt.Errorf("%w; declared models:\n%s", err, modelList())
	}
	return rm, nil
}

// modelList returns every declared model, one per line, sorted.
func modelList() string {
	var b strings.Builder
	for _, name := range models.names() {
		rm := models.byName[name]
		fmt.Fprintf(&b, "%-24s %-16s %-32s ctx=%-8d max_response_tokens=%d\n",
			name, rm.provider.Name, rm.def.Name, rm.def.ContextWindowSize, rm.def.MaxTokens)
	}
	return b.String()
}

// mergeProviderFlags layers --provider JSON definitions over the config
// file's providers: list. A flag whose name matches a file entry replaces
// it in place; a new name is appended. Each flag value is one provider
// object, and every field it omits is omitted — the flag replaces an entry
// rather than patching it, so a partial override would silently drop the
// url or key it left out.
func mergeProviderFlags(file []config.Provider, flags []string) ([]config.Provider, error) {
	if len(flags) == 0 {
		return file, nil
	}
	out := append([]config.Provider(nil), file...)
	for i, raw := range flags {
		var p config.Provider
		if err := yaml.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("--provider[%d]: %w", i, err)
		}
		if p.Name == "" {
			return nil, fmt.Errorf("--provider[%d]: name is required", i)
		}
		if idx := providerIndex(out, p.Name); idx >= 0 {
			out[idx] = p
			continue
		}
		out = append(out, p)
	}
	if err := config.ValidateProviders(out); err != nil {
		return nil, err
	}
	return out, nil
}

func providerIndex(ps []config.Provider, name string) int {
	for i, p := range ps {
		if p.Name == name {
			return i
		}
	}
	return -1
}
