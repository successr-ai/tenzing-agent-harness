package agent

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestDoReasoning_ThinkingBudgetOnRequest(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	// mockLLM reports no model max output, so the budget always rides
	// through and max_tokens grows to keep thinkingHeadroom above it.
	// Clamping against a known model max is covered by TestSizeOutput.
	tests := []struct {
		name    string
		budget  *int64
		want    *int64
		wantMax int64
	}{
		{"unset stays nil", nil, nil, maxTokensStdResponse},
		{"set rides on the request", int64Ptr(8192), int64Ptr(8192), maxTokensStdResponse},
		{"at default cap grows max tokens", int64Ptr(maxTokensStdResponse), int64Ptr(maxTokensStdResponse), maxTokensStdResponse + thinkingHeadroom},
		{"above default cap grows max tokens", int64Ptr(100000), int64Ptr(100000), 100000 + thinkingHeadroom},
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
			if mock.syncReq.MaxTokens != tt.wantMax {
				t.Errorf("MaxTokens = %d, want %d", mock.syncReq.MaxTokens, tt.wantMax)
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
