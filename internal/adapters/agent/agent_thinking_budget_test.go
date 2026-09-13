package agent

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestDoReasoning_ThinkingBudgetOnRequest(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	tests := []struct {
		name   string
		budget *int64
		want   *int64
	}{
		{"unset stays nil", nil, nil},
		{"set rides on the request", int64Ptr(8192), int64Ptr(8192)},
		{"at max tokens clamps below it", int64Ptr(maxTokensStdResponse), int64Ptr(maxTokensStdResponse - 1024)},
		{"above max tokens clamps below it", int64Ptr(100000), int64Ptr(maxTokensStdResponse - 1024)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLLM{syncResponse: common.CompletionResponse{
				StopReason: common.StopReasonEndTurn,
				Content:    []common.ContentBlock{common.NewTextContent("ok")},
			}}
			ag, err := New(AgentConfig{Model: mock, SystemPrompt: "sys", ThinkingBudget: tt.budget})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := ag.DoReasoning(context.Background(), []common.Message{common.NewUserMessage("hi")}, nil, nil); err != nil {
				t.Fatalf("DoReasoning: %v", err)
			}
			got := mock.syncReq.ThinkingBudget
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
