package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

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

// modelsFile is the on-disk shape of models.yaml:
//
//	default: ollama/glm-5.2-cloud
//	models:
//	  - provider: ollama
//	    name: my-local-model
//	    context_window: 128000   # optional
//	    max_tokens: 32768        # optional
//	    base_url: http://box:11434  # optional, applies to the provider
type modelsFile struct {
	Default string       `yaml:"default"`
	Models  []modelEntry `yaml:"models"`
}

type modelEntry struct {
	Provider      string     `yaml:"provider"`
	Name          string     `yaml:"name"`
	ContextWindow int        `yaml:"context_window"`
	MaxTokens     int        `yaml:"max_tokens"`
	BaseURL       string     `yaml:"base_url"`
	Cost          *costEntry `yaml:"cost"`
	// Vision marks the model as accepting image input; image-bearing
	// queries are rejected on models without it.
	Vision bool `yaml:"vision"`
}

// costEntry is USD per million tokens. CacheRead/CacheWrite price prompt-cache
// read and creation tokens; when omitted they default to the Anthropic
// convention (0.1× / 1.25× the input rate) at load time.
type costEntry struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// modelRegistry resolves "provider/name" refs to model definitions:
// custom entries from models.yaml first, then the compiled-in standard
// models from pkg/tenzing.
type modelRegistry struct {
	defaultModel common.ModelDefinition // zero when the file sets no default
	custom       map[string]common.ModelDefinition
	baseURLs     map[string]string
	// pricing is keyed by lowercase model name (matching the model field of
	// LLMResponseEvent); only models.yaml entries with a cost block appear.
	pricing map[string]costEntry
}

// models is the process-wide registry: builtins-only until root.go loads
// models.yaml at startup. Tests exercising loadModelRegistry construct
// their own instances.
var models = emptyRegistry()

// emptyRegistry returns a registry with no custom entries — builtins only.
func emptyRegistry() *modelRegistry {
	return &modelRegistry{
		custom:   map[string]common.ModelDefinition{},
		baseURLs: map[string]string{},
		pricing:  map[string]costEntry{},
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

// loadModelRegistry parses path. A missing file is not an error — it
// returns an empty registry (builtins only, no default override). A file
// that exists but is invalid is a startup error.
func loadModelRegistry(path string) (*modelRegistry, error) {
	reg := emptyRegistry()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return reg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read models config %s: %w", path, err)
	}

	var file modelsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse models config %s: %w", path, err)
	}

	for i, e := range file.Models {
		provider, ok := knownProviders[strings.ToLower(e.Provider)]
		if !ok {
			return nil, fmt.Errorf("models config %s: entry %d: unknown provider %q (known: %s)",
				path, i+1, e.Provider, strings.Join(providerNames(), ", "))
		}
		if e.Name == "" {
			return nil, fmt.Errorf("models config %s: entry %d: name is required", path, i+1)
		}
		def := common.ModelDefinition{
			Name:              e.Name,
			Provider:          provider,
			ContextWindowSize: e.ContextWindow,
			MaxTokens:         e.MaxTokens,
			SupportsVision:    e.Vision,
		}
		if def.ContextWindowSize == 0 {
			def.ContextWindowSize = defaultCustomContextWindow
		}
		if def.MaxTokens == 0 {
			def.MaxTokens = defaultCustomMaxTokens
		}
		reg.custom[modelKey(provider, e.Name)] = def
		if e.BaseURL != "" {
			reg.baseURLs[provider] = e.BaseURL
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

	if file.Default != "" {
		def, err := reg.resolve(file.Default)
		if err != nil {
			return nil, fmt.Errorf("models config %s: default: %w", path, err)
		}
		reg.defaultModel = def
	}
	return reg, nil
}

// resolve maps a "provider/name" ref to a model definition — custom entries
// first, then the compiled-in standard models.
func (r *modelRegistry) resolve(ref string) (common.ModelDefinition, error) {
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
	def, err := models.resolve(strings.TrimSpace(s))
	if err != nil {
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
