package main

import (
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/modelregistry"
	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// deps holds the two model-resolution collaborators every mode shares: the
// registry built from tenzing.yaml (plus --provider overrides) and the LLM
// client factory that caches built clients. RunE builds one pair per run and
// parks it on cfg, so print and serve modes share it and tests inject their
// own instead of seeding process globals.
type deps struct {
	models *modelregistry.Registry
	llms   llmSource
}

// llmSource hands out LLM clients for resolved models. *modelregistry.Factory
// is the production implementation; tests inject a fake so connect mode can
// run end-to-end without a provider.
type llmSource interface {
	Get(rm modelregistry.ResolvedModel) (common.LLM, error)
}

// buildDeps merges --provider JSON definitions over the config file's
// providers, then builds the registry and client factory from the result.
func buildDeps(providers []cfgfile.Provider, entries []cfgfile.ModelEntry, providerFlags []string) (*deps, error) {
	merged, err := modelregistry.MergeProviderFlags(providers, providerFlags)
	if err != nil {
		return nil, err
	}
	reg, err := modelregistry.Build(merged, entries)
	if err != nil {
		return nil, err
	}
	return &deps{models: reg, llms: modelregistry.NewFactory()}, nil
}

// resolve maps a model ref (alias or inline JSON) to a definition, listing
// the declared models on failure — but not for a bad inline definition,
// where the list wouldn't help diagnose the problem.
func (d *deps) resolve(s string) (modelregistry.ResolvedModel, error) {
	ref := strings.TrimSpace(s)
	rm, err := d.models.Resolve(ref)
	if err != nil {
		if strings.HasPrefix(ref, "{") {
			return modelregistry.ResolvedModel{}, err
		}
		return modelregistry.ResolvedModel{}, fmt.Errorf("%w; declared models:\n%s", err, d.models.List())
	}
	return rm, nil
}
