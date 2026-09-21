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
	// judges hands out System One clients for models declared under
	// models.systemone:. systemOneOption builds the one systemone_model:
	// names; the seam is here rather than a feature reaching into the
	// factory.
	judges systemOneSource
}

// llmSource hands out LLM clients for resolved models. *modelregistry.Factory
// is the production implementation; tests inject a fake so connect mode can
// run end-to-end without a provider.
type llmSource interface {
	Get(rm modelregistry.ResolvedModel) (common.LLM, error)
}

// systemOneSource hands out System One clients for resolved decision models,
// the common.SystemOne counterpart to llmSource.
type systemOneSource interface {
	GetSystemOne(rs modelregistry.ResolvedSystemOne) (common.SystemOne, error)
}

// buildDeps merges --provider JSON definitions over the config file's
// providers, then builds the registry and client factory from the result.
func buildDeps(providers []cfgfile.Provider, models cfgfile.ModelsSection, providerFlags []string) (*deps, error) {
	merged, err := modelregistry.MergeProviderFlags(providers, providerFlags)
	if err != nil {
		return nil, err
	}
	reg, err := modelregistry.Build(merged, models)
	if err != nil {
		return nil, err
	}
	factory := modelregistry.NewFactory()
	return &deps{models: reg, llms: factory, judges: factory}, nil
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

// resolveSystemOne maps a System One alias to its model and provider,
// listing the declared models on failure the way resolve does.
func (d *deps) resolveSystemOne(s string) (modelregistry.ResolvedSystemOne, error) {
	rs, err := d.models.ResolveSystemOne(strings.TrimSpace(s))
	if err != nil {
		return modelregistry.ResolvedSystemOne{}, fmt.Errorf("%w; declared models:\n%s", err, d.models.List())
	}
	return rs, nil
}
