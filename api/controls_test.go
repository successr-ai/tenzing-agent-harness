package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// The stub-brained test harness doesn't implement the control
// sub-interfaces, so these exercise route plumbing and error mapping; the
// happy paths are covered by harness-level tests on the default brain.
func TestControlEndpointsErrorMapping(t *testing.T) {
	api := newTestServer(t, &answerAgent{})
	api.cfg.ResolveLLM = func(ref string) (common.LLM, error) {
		if ref != "main" {
			return nil, fmt.Errorf("unknown model %q", ref)
		}
		return &stubLLM{}, nil
	}
	api.cfg.ModelNames = func() []string { return []string{"main"} }

	t.Run("model set with bad ref is 400", func(t *testing.T) {
		in := &modelInput{}
		in.Body.Model = "not-a-ref"
		if _, err := api.handleModelSet(context.Background(), nil, in); err == nil {
			t.Fatal("bad model ref should fail")
		}
	})

	t.Run("model set on unsupported brain is conflict", func(t *testing.T) {
		in := &modelInput{}
		in.Body.Model = "main"
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
			if ref == "main" {
				found = true
			}
		}
		if !found {
			t.Errorf("declared model missing from list: %v", out.Body.Models)
		}
	})
}

func TestStatsEndpoint(t *testing.T) {
	api := newTestServer(t, &answerAgent{})

	if got := submit(api, "count some tokens"); got != turnqueue.Started {
		t.Fatalf("query status = %q", got)
	}
	waitFor(t, "turn to complete", idle(api))

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

func TestModelSetWithoutResolver(t *testing.T) {
	api := newTestServer(t, &answerAgent{})
	in := &modelInput{}
	in.Body.Model = "main"
	if _, err := api.handleModelSet(context.Background(), nil, in); err == nil {
		t.Fatal("model set with no ResolveLLM should 400")
	}
	out, err := api.handleModelsList(context.Background(), nil, nil)
	if err != nil || out.Body.Models != nil {
		t.Fatalf("models list with no ModelNames = (%+v, %v)", out, err)
	}
}

func TestClearResumeResetCosts(t *testing.T) {
	api := newTestServer(t, &answerAgent{})
	submit(api, "one")
	waitFor(t, "turn", idle(api))
	waitFor(t, "cost tracked", func() bool { return api.costs.Stats().Calls == 1 })

	out, err := api.handleClear(context.Background(), nil, nil)
	if err != nil || !strings.HasPrefix(out.Body.Status, "context cleared, now ") {
		t.Fatalf("clear = (%+v, %v)", out, err)
	}
	if api.costs.Stats().Calls != 0 {
		t.Error("clear did not reset costs")
	}

	in := &resumeInput{}
	in.Body.ConversationID = "never-existed"
	if _, err := api.handleResume(context.Background(), nil, in); err == nil {
		t.Error("resume of unknown conversation should fail")
	}
}

// Trust endpoints read and write <UserConfigDir>/tenzing/trust.json; point
// the config dir at a temp directory for the test.
func TestTrustEndpoints(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	api := New(ServerConfig{Cwd: "/proj", TrustEnvDefault: "skip"})

	out, err := api.handleTrustGet(context.Background(), nil, nil)
	if err != nil || out.Body.Trusted || out.Body.Source != "default" || out.Body.Cwd != "/proj" {
		t.Fatalf("initial trust = (%+v, %v)", out, err)
	}
	api.cfg.TrustEnvDefault = "trust"
	if out, _ := api.handleTrustGet(context.Background(), nil, nil); !out.Body.Trusted || out.Body.Source != "env" {
		t.Fatalf("env trust = %+v", out.Body)
	}

	in := &trustInput{}
	in.Body.Trusted = false
	set, err := api.handleTrustSet(context.Background(), nil, in)
	if err != nil || set.Body.Trusted || set.Body.Source != "persisted" || set.Body.Note == "" {
		t.Fatalf("trust set = (%+v, %v)", set, err)
	}
	if out, _ := api.handleTrustGet(context.Background(), nil, nil); out.Body.Trusted || out.Body.Source != "persisted" {
		t.Fatalf("persisted decision not honoured: %+v", out.Body)
	}
}
