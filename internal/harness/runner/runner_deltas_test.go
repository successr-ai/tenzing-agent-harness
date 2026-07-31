package runner

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// callbackCapturingAgent records the callbacks NewAgentRunner installs so
// tests can invoke them as the streaming agent would.
type callbackCapturingAgent struct {
	stream   func(string)
	thinking func(string)
}

func (a *callbackCapturingAgent) GetCurrentModel() string { return "" }
func (a *callbackCapturingAgent) UpdateStreamCallback(fn func(string)) {
	a.stream = fn
}
func (a *callbackCapturingAgent) UpdateThinkingCallback(fn func(string)) {
	a.thinking = fn
}
func (a *callbackCapturingAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	return core.ReasoningResult{}, nil
}

// TestDeltaHandlersCarryRunnerID proves the runner tags streamed deltas
// with its own id before invoking the caller's handlers — the correlation
// contract RPC mode relies on.
func TestDeltaHandlersCarryRunnerID(t *testing.T) {
	agent := &callbackCapturingAgent{}

	type delta struct{ runnerID, text string }
	var gotText, gotThinking []delta

	r, err := NewAgentRunner(agent,
		WithID("runner-7"),
		WithToolRegistry(toolport.NewRegistry("")),
		WithContextStore(newTestContextStore()),
		WithTextDeltaHandler(func(runnerID, text string) {
			gotText = append(gotText, delta{runnerID, text})
		}),
		WithThinkingDeltaHandler(func(runnerID, text string) {
			gotThinking = append(gotThinking, delta{runnerID, text})
		}),
	)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	if agent.stream == nil || agent.thinking == nil {
		t.Fatal("callbacks were not installed on the agent")
	}

	agent.stream("hello")
	agent.thinking("hmm")

	want := delta{r.ID(), "hello"}
	if len(gotText) != 1 || gotText[0] != want {
		t.Errorf("text deltas = %+v, want [%+v]", gotText, want)
	}
	wantTh := delta{r.ID(), "hmm"}
	if len(gotThinking) != 1 || gotThinking[0] != wantTh {
		t.Errorf("thinking deltas = %+v, want [%+v]", gotThinking, wantTh)
	}
	if r.ID() != "runner-7" {
		t.Errorf("runner id = %q, want %q", r.ID(), "runner-7")
	}
}
