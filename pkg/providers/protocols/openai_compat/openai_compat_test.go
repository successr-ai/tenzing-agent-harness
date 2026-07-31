package openai_compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestToOpenAIMessages_AssistantToolCalls(t *testing.T) {
	msgs := []common.Message{
		{
			Role: common.RoleAssistant,
			Content: []common.ContentBlock{
				common.NewTextContent("reading it"),
				common.NewToolUseContent("call_01", "read_file", json.RawMessage(`{"path":"main.go"}`)),
			},
		},
	}

	result := toOpenAIMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	raw, err := json.Marshal(result[0])
	if err != nil {
		t.Fatalf("marshal assistant message: %v", err)
	}

	var parsed struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal assistant message: %v", err)
	}

	if parsed.Role != "assistant" {
		t.Errorf("role = %q, want assistant", parsed.Role)
	}
	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("got %d tool_calls, want 1: %s", len(parsed.ToolCalls), raw)
	}
	if parsed.ToolCalls[0].ID != "call_01" {
		t.Errorf("tool call id = %q, want call_01", parsed.ToolCalls[0].ID)
	}
	if parsed.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool call name = %q, want read_file", parsed.ToolCalls[0].Function.Name)
	}
	if !strings.Contains(parsed.ToolCalls[0].Function.Arguments, "main.go") {
		t.Errorf("tool call arguments = %q, want path argument", parsed.ToolCalls[0].Function.Arguments)
	}
}

func TestToOpenAIMessages_ToolResult(t *testing.T) {
	msgs := []common.Message{
		{
			Role:    common.RoleTool,
			Content: []common.ContentBlock{common.NewToolResultContent("call_01", "read_file", "package main")},
		},
	}

	result := toOpenAIMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}

	raw, err := json.Marshal(result[0])
	if err != nil {
		t.Fatalf("marshal tool message: %v", err)
	}
	for _, want := range []string{`"tool"`, `"call_01"`, "package main"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("tool message missing %s: %s", want, raw)
		}
	}
}

func TestCombinedText(t *testing.T) {
	tests := []struct {
		name     string
		blocks   []common.ContentBlock
		expected string
	}{
		{"single text block", []common.ContentBlock{common.NewTextContent("hello")}, "hello"},
		{"single tool use block", []common.ContentBlock{common.NewToolUseContent("id", "tool", nil)}, ""},
		{"single tool result block", []common.ContentBlock{common.NewToolResultContent("id", "tool", "output")}, ""},
		{
			"mixed blocks",
			[]common.ContentBlock{
				common.NewTextContent("a"),
				common.NewToolUseContent("id", "tool", nil),
				common.NewTextContent("b"),
			},
			"ab",
		},
		{"empty", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := common.CombinedText(tt.blocks); got != tt.expected {
				t.Errorf("combinedText() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFromOpenAIFinishReason(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		expected common.StopReason
	}{
		{"stop", "stop", common.StopReasonStop},
		{"length", "length", common.StopReasonMaxTokens},
		{"tool calls", "tool_calls", common.StopReasonToolUse},
		{"passthrough", "content_filter", common.StopReason("content_filter")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromOpenAIFinishReason(tt.reason); got != tt.expected {
				t.Errorf("fromOpenAIFinishReason(%q) = %q, want %q", tt.reason, got, tt.expected)
			}
		})
	}
}
