package harness

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// requestCapturingLLM records the main agent's request and ends the turn.
type requestCapturingLLM struct {
	stubLLM
	req common.CompletionRequest
}

func (c *requestCapturingLLM) SendMessageWithTools(_ context.Context, req common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	c.req = req
	return common.CompletionResponse{
		StopReason: common.StopReasonEndTurn,
		Content:    []common.ContentBlock{common.NewTextContent("done")},
	}, nil
}

func int64Ptr(v int64) *int64 { return &v }

func TestWithThinkingBudgetReachesMainAgentRequest(t *testing.T) {
	tests := []struct {
		name string
		opts []HarnessOption
		want *int64
	}{
		{"unset leaves nil", nil, nil},
		{"zero leaves nil", []HarnessOption{WithThinkingBudget(0)}, nil},
		{"set flows to the request", []HarnessOption{WithThinkingBudget(8192)}, int64Ptr(8192)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redirectHome(t)
			llm := &requestCapturingLLM{}
			h, err := New(llm, append([]HarnessOption{
				WithSystemPrompt("test"),
				WithContextFilesDisabled(),
				WithSubagentDepth(0),
				WithPermissionsDisabled(),
				WithSessionDisabled(),
			}, tt.opts...)...)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			t.Cleanup(h.Shutdown)
			if _, err := h.RunTurn(context.Background(), "hi"); err != nil {
				t.Fatalf("RunTurn: %v", err)
			}
			got := llm.req.ThinkingBudget
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("ThinkingBudget = %d, want nil", *got)
			case tt.want != nil && got == nil:
				t.Errorf("ThinkingBudget = nil, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("ThinkingBudget = %d, want %d", *got, *tt.want)
			}
		})
	}
}
