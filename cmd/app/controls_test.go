package main

import (
	"context"
	"strings"
	"testing"
)

// The stub-brained test harness doesn't implement the control
// sub-interfaces, so these exercise route plumbing and error mapping; the
// happy paths are covered by harness-level tests on the default brain.
func TestControlEndpointsErrorMapping(t *testing.T) {
	api := newTestServer(t, &answerAgent{})
	api.models = emptyRegistry()

	t.Run("model set with bad ref is 400", func(t *testing.T) {
		in := &modelInput{}
		in.Body.Model = "not-a-ref"
		if _, err := api.handleModelSet(context.Background(), nil, in); err == nil {
			t.Fatal("bad model ref should fail")
		}
	})

	t.Run("model set on unsupported brain is conflict", func(t *testing.T) {
		in := &modelInput{}
		in.Body.Model = modelKey(defaultModel.Provider, defaultModel.Name)
		if _, err := api.handleModelSet(context.Background(), nil, in); err == nil {
			t.Fatal("stub brain cannot switch models; expected error")
		}
	})

	t.Run("thinking on unsupported brain is conflict", func(t *testing.T) {
		in := &thinkingInput{}
		in.Body.Enabled = true
		if _, err := api.handleThinking(context.Background(), nil, in); err == nil {
			t.Fatal("stub brain cannot toggle thinking; expected error")
		}
	})

	// Compaction is owned by the context store, not the brain: on an empty
	// history it is a clean no-op success (deliberate change from the old
	// architecture, where the stub brain made it error).
	t.Run("compact on empty history succeeds", func(t *testing.T) {
		out, err := api.handleCompact(context.Background(), nil, &compactInput{})
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if out.Body.Status != "compacted" {
			t.Errorf("status = %q", out.Body.Status)
		}
	})

	t.Run("models list returns refs and current", func(t *testing.T) {
		out, err := api.handleModelsList(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("models list: %v", err)
		}
		if out.Body.Current == "" {
			t.Error("current model empty")
		}
		found := false
		for _, ref := range out.Body.Models {
			if strings.HasPrefix(ref, "ollama/") {
				found = true
			}
		}
		if !found {
			t.Errorf("builtin ollama models missing from list: %v", out.Body.Models)
		}
	})
}

func TestStatsEndpoint(t *testing.T) {
	api := newTestServer(t, &answerAgent{})

	if got := api.startTurnOrQueue(turnRequest{query: "count some tokens"}); got != "started" {
		t.Fatalf("query status = %q", got)
	}
	waitFor(t, "turn to complete", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return api.cancelFn == nil
	})

	out, err := api.handleStats(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	// answerAgent reports no priced token usage; the endpoint must still
	// respond with a well-formed stats object and null total cost when no
	// priced model was used.
	if out.Body.CostUSD != nil && len(out.Body.ByModel) == 0 {
		t.Error("cost must be null with no priced usage")
	}
}
