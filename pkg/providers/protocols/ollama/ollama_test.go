package ollama

import (
	"encoding/json"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestToOllamaMessages_AssistantToolCalls(t *testing.T) {
	req := common.CompletionRequest{
		Messages: []common.Message{
			{
				Role: common.RoleAssistant,
				Content: []common.ContentBlock{
					common.NewTextContent("reading it"),
					common.NewToolUseContent("call_01", "read_file", json.RawMessage(`{"path":"main.go"}`)),
				},
			},
		},
	}

	msgs := toOllamaMessages(req)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	msg := msgs[0]
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	if msg.Content != "reading it" {
		t.Errorf("content = %q, want %q", msg.Content, "reading it")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool call name = %q, want read_file", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments["path"] != "main.go" {
		t.Errorf("tool call arguments = %v, want path=main.go", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestToOllamaMessages_RolesAndToolResults(t *testing.T) {
	req := common.CompletionRequest{
		System: "be helpful",
		Messages: []common.Message{
			common.NewUserMessage("hi"),
			common.NewAssistantMessage("hello"),
			{
				Role:    common.RoleTool,
				Content: []common.ContentBlock{common.NewToolResultContent("call_01", "read_file", "package main")},
			},
		},
	}

	msgs := toOllamaMessages(req)
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}

	expected := []struct{ role, content string }{
		{"system", "be helpful"},
		{"user", "hi"},
		{"assistant", "hello"},
		{"tool", "package main"},
	}
	for i, want := range expected {
		if msgs[i].Role != want.role || msgs[i].Content != want.content {
			t.Errorf("msg[%d] = {%q, %q}, want {%q, %q}",
				i, msgs[i].Role, msgs[i].Content, want.role, want.content)
		}
	}
	if msgs[3].ToolName != "read_file" {
		t.Errorf("tool message ToolName = %q, want %q", msgs[3].ToolName, "read_file")
	}
}

func TestFromOllamaResponse_ToolCalls(t *testing.T) {
	res := ollamaChatResponse{
		Model: "qwen3.5:9b",
		Message: ollamaChatMessage{
			Role: "assistant",
			ToolCalls: []ollamaToolCall{
				{Function: ollamaToolCallFunction{
					Name:      "read_file",
					Arguments: map[string]any{"path": "main.go"},
				}},
			},
		},
		Done:            true,
		DoneReason:      "stop",
		PromptEvalCount: 10,
		EvalCount:       5,
	}

	out := fromOllamaResponse(res)
	if out.StopReason != common.StopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", out.StopReason, common.StopReasonToolUse)
	}
	calls := out.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].ToolName != "read_file" {
		t.Errorf("tool name = %q, want read_file", calls[0].ToolName)
	}
	if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want 10/5", out.Usage)
	}
}

func TestStripThinkBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no think block", "hello", "hello"},
		{"think block stripped", "<think>reasoning</think>answer", "answer"},
		{"multiline think block", "<think>line1\nline2</think>\nanswer", "answer"},
		{"empty after strip", "<think>only thoughts</think>", ""},
		// GLM/Qwen templates put the opening <think> in the prompt, so the
		// completion contains bare closers only.
		{"orphan closer", "reasoning</think>answer", "answer"},
		{"multiple orphan closers", "step one</think>step two</think>answer", "answer"},
		{"orphan closer only thoughts", "reasoning</think>", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripThinkBlocks(tt.input); got != tt.expected {
				t.Errorf("StripThinkBlocks(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSplitThinkBlocks(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantThinking string
		wantText     string
	}{
		{"no tags", "hello", "", "hello"},
		{"paired block", "<think>reasoning</think>answer", "reasoning", "answer"},
		{"orphan closer", "reasoning</think>answer", "reasoning", "answer"},
		{
			"multiple orphan closers keep full chain of thought",
			"step one</think>step two</think>answer",
			"step one</think>step two",
			"answer",
		},
		{"thoughts only", "<think>reasoning</think>", "reasoning", ""},
		{"empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thinking, text := splitThinkBlocks(tt.input)
			if thinking != tt.wantThinking {
				t.Errorf("thinking = %q, want %q", thinking, tt.wantThinking)
			}
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
		})
	}
}

// Thinking must be classified as ContentTypeThinking blocks, never dropped and
// never leaked into Text() — whether it arrives in Ollama's native `thinking`
// field or inline as (possibly unpaired) <think> tags.
func TestFromOllamaResponse_ThinkingClassified(t *testing.T) {
	tests := []struct {
		name         string
		message      ollamaChatMessage
		wantThinking string
		wantText     string
	}{
		{
			"native thinking field",
			ollamaChatMessage{Role: "assistant", Content: "answer", Thinking: "reasoning"},
			"reasoning",
			"answer",
		},
		{
			"inline paired tags",
			ollamaChatMessage{Role: "assistant", Content: "<think>reasoning</think>answer"},
			"reasoning",
			"answer",
		},
		{
			"inline orphan closer",
			ollamaChatMessage{Role: "assistant", Content: "reasoning</think>answer"},
			"reasoning",
			"answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := fromOllamaResponse(ollamaChatResponse{
				Model:      "glm-5.2:cloud",
				Message:    tt.message,
				Done:       true,
				DoneReason: "stop",
			})
			if got := out.Thinking(); got != tt.wantThinking {
				t.Errorf("Thinking() = %q, want %q", got, tt.wantThinking)
			}
			if got := out.Text(); got != tt.wantText {
				t.Errorf("Text() = %q, want %q", got, tt.wantText)
			}
		})
	}
}

func TestFromOllamaStopReason(t *testing.T) {
	tests := []struct {
		name     string
		res      ollamaChatResponse
		expected common.StopReason
	}{
		{"stop", ollamaChatResponse{DoneReason: "stop"}, common.StopReasonStop},
		{"length", ollamaChatResponse{DoneReason: "length"}, common.StopReasonMaxTokens},
		{
			"tool calls win over done reason",
			ollamaChatResponse{
				DoneReason: "stop",
				Message:    ollamaChatMessage{ToolCalls: []ollamaToolCall{{}}},
			},
			common.StopReasonToolUse,
		},
		{"passthrough", ollamaChatResponse{DoneReason: "load"}, common.StopReason("load")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromOllamaStopReason(tt.res); got != tt.expected {
				t.Errorf("fromOllamaStopReason() = %q, want %q", got, tt.expected)
			}
		})
	}
}
