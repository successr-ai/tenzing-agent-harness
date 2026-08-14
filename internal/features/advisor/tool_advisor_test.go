package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// stubLLM returns a canned response (or error) and records the last request.
type stubLLM struct {
	response    common.CompletionResponse
	err         error
	windowSize  int
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

func (s *stubLLM) GetContextWindowSize() int {
	if s.windowSize > 0 {
		return s.windowSize
	}
	return 128000
}

func (s *stubLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "stub-model", ContextWindowSize: 128000, SupportsVision: true}
}

func staticHistory(msgs []common.Message) HistoryFunc {
	return func(context.Context) ([]common.Message, error) { return msgs, nil }
}

func execute(t *testing.T, llm common.LLM, history HistoryFunc, input string) core.ToolResult {
	t.Helper()
	tool := NewAdvisorTool(llm, history)
	result, err := tool.Execute(context.Background(), tooldef.ExecutionContext{Arguments: []string{input}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

func adviceLLM() *stubLLM {
	return &stubLLM{response: common.CompletionResponse{
		Content: []common.ContentBlock{common.NewTextContent("advice text")},
	}}
}

func TestAdvisorTool_Execute(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "no arguments (general review)",
			input:      "",
			wantOutput: "advice text",
		},
		{
			name:       "empty object",
			input:      `{}`,
			wantOutput: "advice text",
		},
		{
			name:       "with question",
			input:      `{"question":"is the migration order safe?"}`,
			wantOutput: "advice text",
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
	}

	history := staticHistory([]common.Message{common.NewUserMessage("build a worker pool")})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := execute(t, adviceLLM(), history, tt.input)

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
	llm := adviceLLM()
	history := staticHistory([]common.Message{common.NewUserMessage("build a worker pool")})
	execute(t, llm, history, `{"question":"the specific question"}`)

	req := llm.lastRequest
	if req.Model != "advisor-model" {
		t.Errorf("Model = %q, want advisor-model", req.Model)
	}
	if req.System == "" {
		t.Error("System prompt is empty; advisor needs an advisory system prompt")
	}
	if req.MaxTokens != maxTokensStdResponse {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, maxTokensStdResponse)
	}
	if req.Think == nil || *req.Think {
		t.Error("Think = true or nil, want false (reasoning would starve the response budget)")
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages = %d, want 1", len(req.Messages))
	}
	body := req.Messages[0].Content[0].Text
	if !strings.Contains(body, "build a worker pool") {
		t.Errorf("user message missing transcript content: %q", body)
	}
	if !strings.Contains(body, "the specific question") {
		t.Errorf("user message missing question: %q", body)
	}
}

func TestAdvisorTool_TranscriptRendering(t *testing.T) {
	longOutput := strings.Repeat("x", maxRenderedResultChars+100)
	history := staticHistory([]common.Message{
		common.NewUserMessage("the task"),
		{Role: common.RoleAssistant, Content: []common.ContentBlock{
			common.NewThinkingContent("secret draft reasoning"),
			common.NewTextContent("assistant narration"),
			common.NewToolUseContent("t1", "bash", []byte(`{"command":"ls"}`)),
		}},
		{Role: common.RoleTool, Content: []common.ContentBlock{
			common.NewToolResultContent("t1", "bash", longOutput),
		}},
	})

	llm := adviceLLM()
	execute(t, llm, history, "")
	body := llm.lastRequest.Messages[0].Content[0].Text

	for _, want := range []string{"the task", "assistant narration", "bash", `{"command":"ls"}`, "[truncated]"} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "secret draft reasoning") {
		t.Errorf("transcript includes thinking block:\n%s", body)
	}
	if strings.Contains(body, longOutput) {
		t.Error("tool result not truncated")
	}
}

func TestAdvisorTool_TruncatesOldestFirst(t *testing.T) {
	// Window barely above headroom: budget keeps only the newest messages.
	history := staticHistory([]common.Message{
		common.NewUserMessage("OLDEST " + strings.Repeat("a", 400)),
		common.NewUserMessage("MIDDLE " + strings.Repeat("b", 400)),
		common.NewUserMessage("NEWEST message"),
	})
	llm := adviceLLM()
	llm.windowSize = 100 // budget floors at window/2 → ~200 chars

	execute(t, llm, history, "")
	body := llm.lastRequest.Messages[0].Content[0].Text

	if !strings.Contains(body, "NEWEST message") {
		t.Errorf("newest message dropped:\n%s", body)
	}
	if strings.Contains(body, "OLDEST") {
		t.Errorf("oldest message survived truncation:\n%s", body)
	}
	if !strings.Contains(body, "[earlier conversation truncated]") {
		t.Errorf("missing truncation marker:\n%s", body)
	}
}

func TestAdvisorTool_EmptyAndNilHistory(t *testing.T) {
	for _, tt := range []struct {
		name    string
		history HistoryFunc
	}{
		{"nil history func", nil},
		{"empty history", staticHistory(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			llm := adviceLLM()
			result := execute(t, llm, tt.history, "")
			if result.IsError {
				t.Fatalf("IsError = true, want success: %q", result.Output)
			}
			if !strings.Contains(llm.lastRequest.Messages[0].Content[0].Text, "(no conversation yet)") {
				t.Error("empty transcript placeholder missing")
			}
		})
	}
}

func TestAdvisorTool_HistoryError(t *testing.T) {
	history := func(context.Context) ([]common.Message, error) {
		return nil, errors.New("store unavailable")
	}
	result := execute(t, adviceLLM(), history, "")
	if !result.IsError {
		t.Fatal("IsError = false, want true on history failure")
	}
	if !strings.Contains(result.Output, "store unavailable") {
		t.Errorf("Output = %q, want history error", result.Output)
	}
}

func TestAdvisorTool_LLMError(t *testing.T) {
	llm := &stubLLM{err: errors.New("model overloaded")}
	result := execute(t, llm, staticHistory(nil), "")

	if !result.IsError {
		t.Fatal("IsError = false, want true on LLM failure")
	}
	if !strings.Contains(result.Output, "model overloaded") {
		t.Errorf("Output = %q, want it to contain the LLM error", result.Output)
	}
}

// TestAdvisorTool_LiveStoreTranscript wires the advisor to a real
// contextstore.Store (the same accessor the harness registers) and verifies
// the full appended conversation reaches the advisor request.
func TestAdvisorTool_LiveStoreTranscript(t *testing.T) {
	store := contextstore.New(contextstore.Config{})
	ctx := context.Background()

	if err := store.AppendUser(ctx, "fix the flaky auth test"); err != nil {
		t.Fatal(err)
	}
	assistant := common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewTextContent("I'll look at the test first."),
		common.NewToolUseContent("tu-1", "bash", json.RawMessage(`{"command":"go test ./auth/..."}`)),
	}}
	if err := store.AppendAssistant(ctx, assistant); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendToolResults(ctx, []core.ToolResult{
		{ToolUseID: "tu-1", Output: "--- FAIL: TestLogin (0.03s)"},
	}); err != nil {
		t.Fatal(err)
	}

	llm := adviceLLM()
	execute(t, llm, store.Messages, "")

	body := llm.lastRequest.Messages[0].Content[0].Text
	for _, want := range []string{
		"fix the flaky auth test",
		"I'll look at the test first.",
		`bash({"command":"go test ./auth/..."})`,
		"--- FAIL: TestLogin (0.03s)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live-store transcript missing %q:\n%s", want, body)
		}
	}
}
