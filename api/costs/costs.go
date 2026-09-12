// Package costs accumulates token usage, and dollar cost where pricing is
// known, from the harness's LLM response events.
package costs

import (
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Tracker accumulates token usage (and dollar cost where pricing is known)
// from every LLMResponseEvent — main agent and subagents alike. Models
// without pricing report a null cost, never zero. Safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	pricing map[string]config.CostEntry // lowercase model name → USD per MTok
	byModel map[string]*ModelUsage
}

// ModelUsage is the running usage for one model.
type ModelUsage struct {
	// Calls is the number of LLM responses seen for the model.
	Calls int `json:"calls"`
	// InputTokens is the cumulative prompt token count.
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens is the cumulative completion token count.
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadInputTokens is the cumulative count of prompt tokens served
	// from a provider prompt cache.
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	// CacheCreationInputTokens is the cumulative count of prompt tokens
	// written to a provider prompt cache.
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// CostUSD is the running cost in US dollars, or nil when the model has
	// no pricing.
	CostUSD *float64 `json:"cost_usd"`
}

// Stats is a point-in-time snapshot of usage across all models.
type Stats struct {
	// InputTokens is the sum of InputTokens over ByModel.
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens is the sum of OutputTokens over ByModel.
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadInputTokens is the sum of CacheReadInputTokens over ByModel.
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	// CacheCreationInputTokens is the sum of CacheCreationInputTokens over
	// ByModel.
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// Calls is the sum of Calls over ByModel.
	Calls int `json:"calls"`
	// CostUSD is the total cost, or nil when any used model is unpriced (a
	// partial total would understate spend).
	CostUSD *float64 `json:"cost_usd"`
	// ByModel is the per-model breakdown keyed by lowercase model name.
	ByModel map[string]*ModelUsage `json:"by_model"`
}

// NewTracker builds a tracker priced by pricing (lowercase model name → USD
// per MTok). A nil map prices nothing.
func NewTracker(pricing map[string]config.CostEntry) *Tracker {
	if pricing == nil {
		pricing = map[string]config.CostEntry{}
	}
	return &Tracker{pricing: pricing, byModel: map[string]*ModelUsage{}}
}

// Reset zeroes usage, for /clear and /resume: the counters describe the
// conversation in the window, and that conversation just changed. Pricing
// is configuration, not usage, so it survives.
func (c *Tracker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byModel = map[string]*ModelUsage{}
}

// Track adds one LLM response's usage to its model's running totals.
func (c *Tracker) Track(e core.LLMResponseEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(e.Model)
	u := c.byModel[key]
	if u == nil {
		u = &ModelUsage{}
		c.byModel[key] = u
	}
	u.Calls++
	u.InputTokens += e.InputTokens
	u.OutputTokens += e.OutputTokens
	u.CacheReadInputTokens += e.CacheReadInputTokens
	u.CacheCreationInputTokens += e.CacheCreationInputTokens
	if p, ok := c.pricing[key]; ok {
		cost := price(u, p)
		u.CostUSD = &cost
	}
}

// price computes the dollar cost of usage u at rate p.
func price(u *ModelUsage, p config.CostEntry) float64 {
	return float64(u.InputTokens)/1e6*p.Input +
		float64(u.OutputTokens)/1e6*p.Output +
		float64(u.CacheReadInputTokens)/1e6*p.CacheRead +
		float64(u.CacheCreationInputTokens)/1e6*p.CacheWrite
}

// Stats returns a deep copy safe for concurrent marshaling.
func (c *Tracker) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Stats{ByModel: make(map[string]*ModelUsage, len(c.byModel))}
	total := 0.0
	allPriced := true
	for name, u := range c.byModel {
		cp := *u
		if u.CostUSD != nil {
			v := *u.CostUSD
			cp.CostUSD = &v
			total += v
		} else {
			allPriced = false
		}
		out.ByModel[name] = &cp
		out.InputTokens += u.InputTokens
		out.OutputTokens += u.OutputTokens
		out.CacheReadInputTokens += u.CacheReadInputTokens
		out.CacheCreationInputTokens += u.CacheCreationInputTokens
		out.Calls += u.Calls
	}
	if allPriced && len(out.ByModel) > 0 {
		out.CostUSD = &total
	}
	return out
}
