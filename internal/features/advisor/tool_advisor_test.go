package advisor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// stubLLM returns a canned response (or error) and records the last request.
type stubLLM struct {
	response    common.CompletionResponse
	err         error
	lastRequest common.CompletionRequest
}

func (s *stubLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	s.lastRequest = req
	return s.response, s.err
}

func (s *stubLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, events chan<- common.StreamEvent) error {
	close(events)
	return nil
}

func (s *stubLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}

func (s *stubLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}

func (s *stubLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (s *stubLLM) GetCurrentModel() string                                  { return "advisor-model" }
func (s *stubLLM) GetContextWindowSize() int                                { return 128000 }

func (s *stubLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "stub-model", ContextWindowSize: 128000, SupportsVision: true}
}

func execute(t *testing.T, llm common.LLM, input string) core.ToolResult {
	t.Helper()
	tool := NewAdvisorTool(llm)
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{Arguments: []string{input}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

func TestAdvisorTool_Execute(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "valid plan",
			input:      `{"plan":"refactor the parser in one pass"}`,
			wantOutput: "advice text",
		},
		{
			name:       "plan with context",
			input:      `{"plan":"migrate DB","context":"postgres 14, zero downtime required"}`,
			wantOutput: "advice text",
		},
		{
			name:    "missing plan",
			input:   `{"context":"background only"}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "empty arguments",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &stubLLM{response: common.CompletionResponse{
				Content: []common.ContentBlock{common.NewTextContent("advice text")},
			}}
			result := execute(t, llm, tt.input)

			if result.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, want %v (output: %q)", result.IsError, tt.wantErr, result.Output)
			}
			if !tt.wantErr && result.Output != tt.wantOutput {
				t.Errorf("Output = %q, want %q", result.Output, tt.wantOutput)
			}
		})
	}
}

func TestAdvisorTool_RequestShape(t *testing.T) {
	llm := &stubLLM{response: common.CompletionResponse{
		Content: []common.ContentBlock{common.NewTextContent("ok")},
	}}
	execute(t, llm, `{"plan":"the plan body","context":"the task context"}`)

	req := llm.lastRequest
	if req.Model != "advisor-model" {
		t.Errorf("Model = %q, want advisor-model", req.Model)
	}
	if req.System == "" {
		t.Error("System prompt is empty; advisor needs an advisory system prompt")
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(req.Messages))
	}
	body := req.Messages[0].Content[0].Text
	if !strings.Contains(body, "the plan body") {
		t.Errorf("user message missing plan: %q", body)
	}
	if !strings.Contains(body, "the task context") {
		t.Errorf("user message missing context: %q", body)
	}
	if req.MaxTokens <= 0 {
		t.Errorf("MaxTokens = %d, want > 0", req.MaxTokens)
	}
}

func TestAdvisorTool_LLMError(t *testing.T) {
	llm := &stubLLM{err: errors.New("model overloaded")}
	result := execute(t, llm, `{"plan":"anything"}`)

	if !result.IsError {
		t.Fatal("IsError = false, want true on LLM failure")
	}
	if !strings.Contains(result.Output, "model overloaded") {
		t.Errorf("Output = %q, want it to contain the LLM error", result.Output)
	}
}
