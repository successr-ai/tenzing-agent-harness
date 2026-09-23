package main

import (
	"fmt"
	"os"
	"strings"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// systemOneOption builds the decision-model harness option from
// systemone_model: and the systemone: block. It returns a nil option when no
// decision model is named, which is the common case.
//
// The block's fields are pointers, so an unset one keeps the feature's own
// default and an explicit zero survives. Two defaults are decided here rather
// than in the feature, because only this layer knows them: the message tail
// (0 means "send none", so the feature cannot default it) and whether the
// advisor consumer has anything to consult.
func systemOneOption(cfg *cliConfig) (harness.HarnessOption, error) {
	if cfg.SystemOneModel == "" {
		return nil, nil
	}
	rs, err := cfg.deps.resolveSystemOne(cfg.SystemOneModel)
	if err != nil {
		return nil, fmt.Errorf("--systemone-model: %w", err)
	}
	client, err := cfg.deps.judges.GetSystemOne(rs)
	if err != nil {
		return nil, fmt.Errorf("--systemone-model: %w", err)
	}

	sc := systemone.Config{
		Client:         client,
		RecentMessages: systemone.DefaultRecentMessages,
		// Debug aid, env-only on purpose: the file holds conversation
		// content, and a config key would make leaving it on too easy.
		CaptureFile: os.Getenv("TENZING_SYSTEMONE_CAPTURE"),
	}
	// Nothing to consult without the advisor tool, which advisor_model:
	// mounts.
	sc.Advisor.Disabled = cfg.AdvisorModel == ""
	candidates := cfg.RoutingCandidates

	if b := cfg.SystemOne; b != nil {
		if v := b.RecentMessages; v != nil {
			sc.RecentMessages = *v
		}
		if v := b.Gate.Enabled; v != nil && !*v {
			sc.Gate.Disabled = true
		}
		if v := b.Gate.IrreversibleAsk; v != nil {
			sc.Gate.IrreversibleAsk = *v
		}
		if v := b.Gate.SecretsAsk; v != nil {
			sc.Gate.SecretsAsk = *v
		}
		switch v := b.Advisor.Enabled; {
		case v == nil:
		case !*v:
			sc.Advisor.Disabled = true
		case cfg.AdvisorModel == "":
			fmt.Fprintf(os.Stderr, "warning: systemone.advisor.enabled requires advisor_model; ignored\n")
		default:
			sc.Advisor.Disabled = false
		}
		if v := b.Routing.Enabled; v != nil && !*v {
			candidates = nil
		}
		if v := b.Routing.MinConfidence; v != nil {
			sc.Routing.MinConfidence = *v
		}
	}

	// Routing needs a choice to make and a way to apply it. Fewer than two
	// described models is not a choice — config validation rejects asking
	// for routing explicitly in that case, and leaves it off otherwise.
	var resolveModel func(alias string) (common.LLM, error)
	if len(candidates) >= 2 {
		sc.Routing.Candidates = candidates
		sc.Routing.Current = cfg.Model
		resolveModel = func(alias string) (common.LLM, error) {
			rm, err := cfg.deps.resolve(alias)
			if err != nil {
				return nil, err
			}
			return cfg.deps.llms.Get(rm)
		}
	}
	return harness.WithSystemOne(sc, resolveModel), nil
}

// routingCandidates collects the chat models eligible for routing: the ones
// whose entry carries a description, which is what the decision model is told
// the option means.
func routingCandidates(entries []cfgfile.ModelEntry) []systemone.Candidate {
	var out []systemone.Candidate
	for _, e := range entries {
		if d := strings.TrimSpace(e.Description); d != "" {
			out = append(out, systemone.Candidate{Name: e.Name, Description: d})
		}
	}
	return out
}

// systemOneSummary is the extra --list-models block: which decision model is
// in play and what routing may choose between. Empty when none is named.
func systemOneSummary(f cfgfile.File) string {
	if f.SystemOneModel == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nsystemone_model: %s\n", f.SystemOneModel)
	names := make([]string, 0, len(f.Models.LLM))
	for _, c := range routingCandidates(f.Models.LLM) {
		names = append(names, c.Name)
	}
	switch {
	case len(names) >= 2:
		fmt.Fprintf(&b, "routing candidates: %s\n", strings.Join(names, ", "))
	case len(names) == 1:
		fmt.Fprintf(&b, "routing candidates: %s (routing needs two; add a description: to another models.llm entry)\n", names[0])
	default:
		b.WriteString("routing candidates: none (add a description: to two or more models.llm entries)\n")
	}
	return b.String()
}
