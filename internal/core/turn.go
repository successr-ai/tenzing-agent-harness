// Package core holds the invariant agent loop, its domain types, port
// interfaces, and events. It imports nothing from internal/ — adapters and
// extensions import core, never the reverse.
package core

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// ProviderToolDefinition is the provider-level tool definition surfaced
// through ToolPort. Aliased so extensions can build ToolSpecs against core
// without naming the provider package.
type ProviderToolDefinition = common.ToolDefinition

// ToolSpec is an origin-tagged tool mounted into the composite ToolPort.
// It carries its own Execute closure so extension tools need no central
// registry registration.
type ToolSpec struct {
	Definition ProviderToolDefinition
	Origin     string // "native", "mcp:<server>", "extension:<name>"
	// ReadOnly marks a tool that performs no mutations; the loop may run it
	// concurrently with adjacent read-only calls. Defaults to false.
	ReadOnly bool
	Execute  func(ctx context.Context, call ToolCall) ToolResult
}

// ToolCall is one tool_use request from the model.
type ToolCall struct {
	ID    string
	Name  string
	Input string
}

// ToolResult is the outcome of executing one ToolCall. Failures are data
// (IsError), never Go errors — the model reacts to them.
type ToolResult struct {
	ToolUseID string
	Output    string
	IsError   bool
	Metadata  map[string]string
}

// ResponseMeta carries per-call metadata about one ModelPort.DoReasoning
// invocation, for logging/events.
type ResponseMeta struct {
	Model        string
	ResponseID   string
	InputTokens  int64
	OutputTokens int64
	// CacheReadInputTokens / CacheCreationInputTokens report prompt-cache
	// usage (Anthropic; zero elsewhere).
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	StopReason               string
	AssistantText            string
	// AssistantMessage is the full assistant message (all content blocks,
	// including tool_use) as produced by the model. The runner appends it
	// to the ContextPort — the model itself is stateless.
	AssistantMessage common.Message
}

// ReasoningResult is the outcome of one ModelPort.DoReasoning call.
type ReasoningResult struct {
	// ToolCalls holds every tool_use block from the response, in order. The
	// runner must execute all of them and feed the results back in the same
	// order so each pairs with its tool_use id.
	ToolCalls   []ToolCall
	FinalAnswer string
	Meta        ResponseMeta
}
