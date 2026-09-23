package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"go.yaml.in/yaml/v3"
)

// AdvisorCase is one labelled moment in a turn: what the agent has been
// doing, and whether its advisor should see it before the next action. The
// label is a fact about the moment; whether the harness blocks is derived
// from it, as with the gate's cases.
type AdvisorCase struct {
	Name string `yaml:"name"`
	// Request is the turn's opening user message; Assistant is the text of
	// the agent's recent messages, newest last, no more than the live tail
	// (recent_messages, default 4) would carry. Tool calls and results are
	// not in batch A's state, so they are not here: write only what the
	// agent said.
	Request         string   `yaml:"request"`
	Assistant       []string `yaml:"assistant"`
	Iteration       int      `yaml:"iteration"`
	ElapsedSeconds  int      `yaml:"elapsed_seconds"`
	AdvisorConsults int      `yaml:"advisor_consults"`
	NeedsAdvisor    bool     `yaml:"needs_advisor"`
	KnownGap        string   `yaml:"known_gap"`
}

// State is the batch A state the harness would send at this moment.
func (c AdvisorCase) State() any {
	return systemone.TurnState(c.Iteration, c.ElapsedSeconds, c.AdvisorConsults, c.Request, c.Assistant)
}

// LoadAdvisorCases reads and validates the advisor fixtures, as strictly as
// LoadCases and for the same reason.
func LoadAdvisorCases(path string) ([]AdvisorCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Cases []AdvisorCase `yaml:"cases"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	seen := make(map[string]bool, len(f.Cases))
	for i, c := range f.Cases {
		switch {
		case c.Name == "":
			return nil, fmt.Errorf("%s: cases[%d]: name is required", path, i)
		case seen[c.Name]:
			return nil, fmt.Errorf("%s: cases[%d]: duplicate name %q", path, i, c.Name)
		case c.Request == "":
			return nil, fmt.Errorf("%s: %q: request is required — the live state always carries it", path, c.Name)
		case len(c.Assistant) > systemone.DefaultRecentMessages:
			return nil, fmt.Errorf("%s: %q: %d assistant messages, more than the live tail's %d", path, c.Name, len(c.Assistant), systemone.DefaultRecentMessages)
		case c.Iteration < 1:
			return nil, fmt.Errorf("%s: %q: iteration must be 1 or more", path, c.Name)
		}
		seen[c.Name] = true
	}
	return f.Cases, nil
}

func defaultAdvisorCasesPath() string {
	return filepath.Join(filepath.Dir(defaultCasesPath()), "advisor.yaml")
}

// runAdvisor measures the advisor-need question against its fixtures and
// reports the same way the gate run does: failures and tight passes, then
// the separation between the two labelled populations.
func runAdvisor(path string, client common.SystemOne, model string, consultAbove float64, verbose bool, qs questionSet) error {
	cases, err := LoadAdvisorCases(path)
	if err != nil {
		return err
	}
	fmt.Printf("%d advisor cases against %s\nquestions: %s\nthreshold: consult above %.2f\n\n", len(cases), model, qs.Name, consultAbove)

	q := qs.Advisor
	probs := make([]float64, len(cases))
	errs := make([]error, len(cases))
	start := time.Now()
	for i, c := range cases {
		resp, err := client.Evaluate(context.Background(), common.EvaluationRequest{
			State:     c.State(),
			Questions: map[string]common.Question{"needs": q},
		})
		if err != nil {
			errs[i] = err
		} else {
			probs[i] = resp.Answers["needs"].Noul
		}
		fmt.Print(".")
	}
	fmt.Printf("\n\nmeasured in %s\n\n", time.Since(start).Round(time.Millisecond))

	var failed, gaps int
	var low, high []float64
	for i, c := range cases {
		p := probs[i]
		if errs[i] != nil {
			failed++
			fmt.Printf("%-6s %-50s %v\n", "ERR", truncate(c.Name, 50), errs[i])
			continue
		}
		if c.NeedsAdvisor {
			high = append(high, p)
		} else {
			low = append(low, p)
		}
		ok := (p > consultAbove) == c.NeedsAdvisor
		margin := consultAbove - p
		if c.NeedsAdvisor {
			margin = p - consultAbove
		}
		mark := ""
		switch {
		case c.KnownGap != "" && !ok:
			gaps++
			mark = "gap"
		case c.KnownGap != "":
			mark = "FIXED"
		case !ok:
			failed++
			mark = "FAIL"
		case margin < 0.1:
			mark = "tight"
		case verbose:
			mark = "PASS"
		}
		if mark != "" {
			fmt.Printf("%-6s %-50s p=%.2f want %-5v %+.2f\n", mark, truncate(c.Name, 50), p, c.NeedsAdvisor, margin)
		}
	}
	fmt.Printf("\n%d/%d passed", len(cases)-failed-gaps, len(cases)-gaps)
	if gaps > 0 {
		fmt.Printf(", %d known gap(s)", gaps)
	}
	fmt.Println()
	fmt.Println()
	gap("needs_advisor", "settled", low, "decision", high)

	if failed > 0 {
		return fmt.Errorf("%d advisor case(s) failed", failed)
	}
	return nil
}
