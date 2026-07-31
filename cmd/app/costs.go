package main

import (
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// costTracker accumulates token usage (and dollar cost where pricing is
// known) from every LLMResponseEvent on the bus — main agent and subagents
// alike. Models without pricing report a null cost, never zero.
type costTracker struct {
	mu      sync.Mutex
	pricing map[string]costEntry // lowercase model name → USD per MTok
	byModel map[string]*modelUsage
}

type modelUsage struct {
	Calls                    int      `json:"calls"`
	InputTokens              int64    `json:"input_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	CacheReadInputTokens     int64    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64    `json:"cache_creation_input_tokens"`
	CostUSD                  *float64 `json:"cost_usd"` // null when the model has no pricing
}

type costStats struct {
	InputTokens              int64                  `json:"input_tokens"`
	OutputTokens             int64                  `json:"output_tokens"`
	CacheReadInputTokens     int64                  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64                  `json:"cache_creation_input_tokens"`
	Calls                    int                    `json:"calls"`
	CostUSD                  *float64               `json:"cost_usd"` // null when any used model is unpriced
	ByModel                  map[string]*modelUsage `json:"by_model"`
}

func newCostTracker(pricing map[string]costEntry) *costTracker {
	if pricing == nil {
		pricing = map[string]costEntry{}
	}
	return &costTracker{pricing: pricing, byModel: map[string]*modelUsage{}}
}

func (c *costTracker) track(e core.LLMResponseEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(e.Model)
	u := c.byModel[key]
	if u == nil {
		u = &modelUsage{}
		c.byModel[key] = u
	}
	u.Calls++
	u.InputTokens += e.InputTokens
	u.OutputTokens += e.OutputTokens
	u.CacheReadInputTokens += e.CacheReadInputTokens
	u.CacheCreationInputTokens += e.CacheCreationInputTokens
	if p, ok := c.pricing[key]; ok {
		cost := float64(u.InputTokens)/1e6*p.Input +
			float64(u.OutputTokens)/1e6*p.Output +
			float64(u.CacheReadInputTokens)/1e6*p.CacheRead +
			float64(u.CacheCreationInputTokens)/1e6*p.CacheWrite
		u.CostUSD = &cost
	}
}

// stats returns a deep copy safe for concurrent marshaling.
func (c *costTracker) stats() costStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := costStats{ByModel: make(map[string]*modelUsage, len(c.byModel))}
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
