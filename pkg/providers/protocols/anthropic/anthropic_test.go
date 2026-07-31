package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestToAnthropicMessages_ToolRoundTrip(t *testing.T) {
	input := json.RawMessage(`{"path":"main.go"}`)
	msgs := []common.Message{
		common.NewUserMessage("read the file"),
		{
			Role: common.RoleAssistant,
			Content: []common.ContentBlock{
				common.NewTextContent("reading it"),
				common.NewToolUseContent("toolu_01", "read_file", input),
			},
		},
		{
			Role:    common.RoleUser,
			Content: []common.ContentBlock{common.NewToolResultContent("toolu_01", "read_file", "package main")},
		},
	}

	result := toAnthropicMessages(msgs)
	if len(result) != 3 {
		t.Fatalf("got %d messages, want 3", len(result))
	}

	assistant, err := json.Marshal(result[1])
	if err != nil {
		t.Fatalf("marshal assistant message: %v", err)
	}
	for _, want := range []string{`"tool_use"`, `"toolu_01"`, `"read_file"`, `"path"`} {
		if !strings.Contains(string(assistant), want) {
			t.Errorf("assistant message missing %s: %s", want, assistant)
		}
	}

	user, err := json.Marshal(result[2])
	if err != nil {
		t.Fatalf("marshal tool result message: %v", err)
	}
	for _, want := range []string{`"tool_result"`, `"toolu_01"`} {
		if !strings.Contains(string(user), want) {
			t.Errorf("tool result message missing %s: %s", want, user)
		}
	}
}

func TestToAnthropicTools_SchemaShape(t *testing.T) {
	tools, err := toAnthropicTools([]common.ToolDefinition{{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"path": {"type": "string"}},
			"required": ["path"]
		}`),
	}})
	if err != nil {
		t.Fatalf("toAnthropicTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	raw, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}

	var parsed struct {
		InputSchema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}

	if parsed.InputSchema.Type != "object" {
		t.Errorf("input_schema.type = %q, want object", parsed.InputSchema.Type)
	}
	if _, ok := parsed.InputSchema.Properties["path"]; !ok {
		t.Errorf("input_schema.properties missing path key: %s", raw)
	}
	// The whole schema nested under properties is the bug this guards against.
	if _, ok := parsed.InputSchema.Properties["properties"]; ok {
		t.Errorf("schema double-nested under properties: %s", raw)
	}
	if len(parsed.InputSchema.Required) != 1 || parsed.InputSchema.Required[0] != "path" {
		t.Errorf("input_schema.required = %v, want [path]", parsed.InputSchema.Required)
	}
}

func TestToAnthropicTools_InvalidSchema(t *testing.T) {
	_, err := toAnthropicTools([]common.ToolDefinition{{
		Name:        "bad",
		InputSchema: json.RawMessage(`{not json`),
	}})
	if err == nil {
		t.Fatal("want error for invalid schema, got nil")
	}
}

func TestFromAnthropicStopReason(t *testing.T) {
	tests := []struct {
		name     string
		reason   anthropicSDK.StopReason
		expected common.StopReason
	}{
		{"end turn", anthropicSDK.StopReasonEndTurn, common.StopReasonEndTurn},
		{"max tokens", anthropicSDK.StopReasonMaxTokens, common.StopReasonMaxTokens},
		{"tool use", anthropicSDK.StopReasonToolUse, common.StopReasonToolUse},
		{"passthrough", anthropicSDK.StopReason("refusal"), common.StopReason("refusal")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromAnthropicStopReason(tt.reason); got != tt.expected {
				t.Errorf("fromAnthropicStopReason(%q) = %q, want %q", tt.reason, got, tt.expected)
			}
		})
	}
}
