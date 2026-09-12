package costs

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func llmEvent(model string, in, out int64) core.LLMResponseEvent {
	return core.LLMResponseEvent{
		BaseEvent:    core.NewBaseEvent(core.EventLLMResponse, "r1"),
		Model:        model,
		InputTokens:  in,
		OutputTokens: out,
	}
}

func TestCostTracker(t *testing.T) {
	ct := NewTracker(map[string]config.CostEntry{
		"priced-model": {Input: 3.0, Output: 15.0}, // USD per MTok
	})

	ct.Track(llmEvent("priced-model", 1_000_000, 100_000))
	ct.Track(llmEvent("priced-model", 500_000, 0))

	s := ct.Stats()
	if s.InputTokens != 1_500_000 || s.OutputTokens != 100_000 || s.Calls != 2 {
		t.Errorf("totals = %+v", s)
	}
	u := s.ByModel["priced-model"]
	if u == nil || u.CostUSD == nil {
		t.Fatal("priced model missing cost")
	}
	want := 1.5*3.0 + 0.1*15.0 // 4.5 + 1.5 = 6.0
	if *u.CostUSD != want {
		t.Errorf("cost = %v, want %v", *u.CostUSD, want)
	}
	if s.CostUSD == nil || *s.CostUSD != want {
		t.Errorf("total cost = %v, want %v", s.CostUSD, want)
	}

	// unknown model: tokens tracked, cost null — and total goes null too
	ct.Track(llmEvent("mystery-model", 10, 10))
	s = ct.Stats()
	if s.ByModel["mystery-model"].CostUSD != nil {
		t.Error("unpriced model must report null cost, not zero")
	}
	if s.CostUSD != nil {
		t.Error("total cost must be null when any used model is unpriced")
	}
	if s.InputTokens != 1_500_010 {
		t.Errorf("input tokens = %d", s.InputTokens)
	}
}

func TestCostTrackerCacheTokens(t *testing.T) {
	ct := NewTracker(map[string]config.CostEntry{
		"cached-model": {Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75},
	})

	e := llmEvent("cached-model", 1_000_000, 100_000)
	e.CacheReadInputTokens = 2_000_000
	e.CacheCreationInputTokens = 400_000
	ct.Track(e)

	s := ct.Stats()
	if s.CacheReadInputTokens != 2_000_000 || s.CacheCreationInputTokens != 400_000 {
		t.Errorf("cache totals = %+v", s)
	}
	u := s.ByModel["cached-model"]
	if u.CacheReadInputTokens != 2_000_000 || u.CacheCreationInputTokens != 400_000 {
		t.Errorf("per-model cache tokens = %+v", u)
	}
	want := 1.0*3.0 + 0.1*15.0 + 2.0*0.3 + 0.4*3.75 // 3 + 1.5 + 0.6 + 1.5 = 6.6
	if u.CostUSD == nil || *u.CostUSD != want {
		t.Errorf("cost = %v, want %v", u.CostUSD, want)
	}
}

func TestNewTrackerNilPricing(t *testing.T) {
	ct := NewTracker(nil)
	ct.Track(llmEvent("m", 1, 1))
	s := ct.Stats()
	if s.Calls != 1 || s.ByModel["m"].CostUSD != nil || s.CostUSD != nil {
		t.Errorf("nil pricing: stats = %+v", s)
	}
}

func TestStatsEmpty(t *testing.T) {
	s := NewTracker(nil).Stats()
	if s.Calls != 0 || s.CostUSD != nil || len(s.ByModel) != 0 {
		t.Errorf("empty tracker: stats = %+v", s)
	}
}

func TestReset(t *testing.T) {
	ct := NewTracker(map[string]config.CostEntry{"m": {Input: 1}})
	ct.Track(llmEvent("m", 1_000_000, 0))
	ct.Reset()
	if s := ct.Stats(); s.Calls != 0 || len(s.ByModel) != 0 {
		t.Errorf("after reset: stats = %+v", s)
	}
	// pricing survives the reset
	ct.Track(llmEvent("M", 1_000_000, 0)) // model names are case-insensitive
	if s := ct.Stats(); s.CostUSD == nil || *s.CostUSD != 1 {
		t.Errorf("after reset, cost = %v, want 1", s.CostUSD)
	}
}

func TestStatsIsDeepCopy(t *testing.T) {
	ct := NewTracker(map[string]config.CostEntry{"m": {Input: 1}})
	ct.Track(llmEvent("m", 1_000_000, 0))
	s := ct.Stats()
	*s.ByModel["m"].CostUSD = 99
	s.ByModel["m"].Calls = 99
	if again := ct.Stats(); *again.ByModel["m"].CostUSD != 1 || again.ByModel["m"].Calls != 1 {
		t.Errorf("mutating a snapshot leaked into the tracker: %+v", again.ByModel["m"])
	}
}
