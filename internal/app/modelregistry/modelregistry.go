// Package modelregistry resolves the model aliases declared in tenzing.yaml
// to concrete model definitions bound to their providers, and builds the LLM
// protocol clients that serve them. It is the composition root's
// model-resolution and provider-construction helper: cmd/app builds one
// Registry and one Factory per run and injects them wherever a model ref
// needs resolving or a client needs constructing.
package modelregistry

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

// ResolvedModel pairs a model definition with the provider instance that
// serves it. The definition alone is not enough to build a client: two
// models can share a provider type but sit behind different endpoints and
// keys, so the provider travels with the model rather than being looked up
// by type later. Def.Name is the wire id protocol clients send; the local
// alias used to resolve it never leaves this package's output.
type ResolvedModel struct {
	// Def is the model definition as protocol clients consume it.
	Def common.ModelDefinition
	// Provider is the declared backend serving the model: the endpoint and
	// key the Factory builds the client from.
	Provider config.Provider
}

// ResolvedSystemOne pairs a System One model with the provider serving it.
// It is deliberately not a common.ModelDefinition: every other field of that
// struct describes a chat model (context window, max tokens, vision,
// reasoning tier), and a decision model has none of them. Name is the wire
// id ("jev-latest" at TypeSafe, "typesafe/jev-1.13" via OpenRouter); the
// local alias never leaves this package's output.
type ResolvedSystemOne struct {
	Name     string
	Provider config.Provider
}

// Registry resolves model refs to definitions. Everything comes from
// tenzing.yaml: models are keyed by the `name` alias they declare, and there
// is no compiled-in fallback set. The two kinds share one alias namespace
// (config.File.validate enforces it) but sit in separate indexes, because a
// ref resolves to one kind or the other, never either.
type Registry struct {
	byName    map[string]ResolvedModel
	systemOne map[string]ResolvedSystemOne
	providers map[string]config.Provider
	// pricing is keyed by lowercase wire model name (matching the model
	// field of LLMResponseEvent, which providers fill in from the response,
	// not from the local alias); only entries with a cost block appear.
	pricing map[string]config.CostEntry
}

// Build builds the Registry from tenzing.yaml's providers: and models:
// sections. config.File.validate has already checked that names are unique
// across both model kinds and that every model names a declared provider of
// the right type, so the work here is defaulting and indexing.
func Build(providers []config.Provider, models config.ModelsSection) (*Registry, error) {
	reg := &Registry{
		byName:    map[string]ResolvedModel{},
		systemOne: map[string]ResolvedSystemOne{},
		providers: map[string]config.Provider{},
		pricing:   map[string]config.CostEntry{},
	}
	for _, p := range providers {
		reg.providers[p.Name] = p
	}

	for i, e := range models.SystemOne {
		prov, ok := reg.providers[e.Provider]
		if !ok {
			return nil, fmt.Errorf("models.systemone[%d]: provider %q is not declared in providers: (declared: %s)",
				i, e.Provider, strings.Join(reg.providerNames(), ", "))
		}
		reg.systemOne[e.Name] = ResolvedSystemOne{Name: e.ModelName, Provider: prov}
	}

	for i, e := range models.LLM {
		rm, err := reg.resolveEntry(e)
		if err != nil {
			return nil, fmt.Errorf("models.llm[%d]: %w", i, err)
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
func (r *Registry) resolveEntry(e config.ModelEntry) (ResolvedModel, error) {
	if e.ModelName == "" {
		return ResolvedModel{}, fmt.Errorf("model_name is required")
	}
	prov, ok := r.providers[e.Provider]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("provider %q is not declared in providers: (declared: %s)",
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
	return ResolvedModel{Def: def, Provider: prov}, nil
}

func (r *Registry) providerNames() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Resolve maps a model ref to a definition and its provider. A ref starting
// with "{" is an inline definition — JSON (or YAML flow) with the model
// entry fields, e.g. {"provider":"ollama-cloud","model_name":"glm-5.3"} —
// validated and defaulted like a models: entry, and naming a provider that
// must already be declared. Otherwise the ref is a model alias.
func (r *Registry) Resolve(ref string) (ResolvedModel, error) {
	if strings.HasPrefix(ref, "{") {
		var e config.ModelEntry
		if err := yaml.Unmarshal([]byte(ref), &e); err != nil {
			return ResolvedModel{}, fmt.Errorf("inline model definition: %w", err)
		}
		rm, err := r.resolveEntry(e)
		if err != nil {
			return ResolvedModel{}, fmt.Errorf("inline model definition: %w", err)
		}
		return rm, nil
	}
	rm, ok := r.byName[ref]
	if !ok {
		if _, isSystemOne := r.systemOne[ref]; isSystemOne {
			return ResolvedModel{}, fmt.Errorf("model %q is a System One model: it answers typed questions, not chat turns, so it cannot serve a model ref", ref)
		}
		return ResolvedModel{}, fmt.Errorf("model %q is not declared in models.llm:", ref)
	}
	return rm, nil
}

// ResolveSystemOne maps an alias to a System One model and its provider.
// Aliases only: inline "{...}" refs exist for --model, and no flag or config
// key takes a System One ref yet.
func (r *Registry) ResolveSystemOne(ref string) (ResolvedSystemOne, error) {
	rs, ok := r.systemOne[ref]
	if !ok {
		if _, isLLM := r.byName[ref]; isLLM {
			return ResolvedSystemOne{}, fmt.Errorf("model %q is a chat model, not a System One model", ref)
		}
		return ResolvedSystemOne{}, fmt.Errorf("model %q is not declared in models.systemone:", ref)
	}
	return rs, nil
}

// SystemOneNames lists every declared System One alias, sorted.
func (r *Registry) SystemOneNames() []string {
	out := make([]string, 0, len(r.systemOne))
	for k := range r.systemOne {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Names lists every declared model alias, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for k := range r.byName {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns every declared model, one per line, sorted — the rendering
// behind --list-models and the declared-models part of a bad-ref error.
func (r *Registry) List() string {
	var b strings.Builder
	if len(r.systemOne) > 0 {
		b.WriteString("llm:\n")
	}
	for _, name := range r.Names() {
		rm := r.byName[name]
		fmt.Fprintf(&b, "%-24s %-16s %-32s ctx=%-8d max_response_tokens=%d\n",
			name, rm.Provider.Name, rm.Def.Name, rm.Def.ContextWindowSize, rm.Def.MaxTokens)
	}
	// System One rows carry no context or token columns: the budgets are
	// protocol constants, and the answer is a fixed-size typed value.
	if len(r.systemOne) > 0 {
		b.WriteString("systemone:\n")
		for _, name := range r.SystemOneNames() {
			rs := r.systemOne[name]
			fmt.Fprintf(&b, "%-24s %-16s %s\n", name, rs.Provider.Name, rs.Name)
		}
	}
	return b.String()
}

// Pricing returns a copy of the cost table keyed by lowercase wire model
// name. Callers get their own map; the registry's table is never shared.
func (r *Registry) Pricing() map[string]config.CostEntry {
	out := make(map[string]config.CostEntry, len(r.pricing))
	for k, v := range r.pricing {
		out[k] = v
	}
	return out
}
