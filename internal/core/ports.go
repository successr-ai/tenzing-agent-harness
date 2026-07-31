package core

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// ContextPort owns the conversation history: user/assistant messages, tool
// result pairing, and compression. Adapters implement it; the core loop
// depends only on this interface.
type ContextPort interface {
	Messages(ctx context.Context) ([]common.Message, error)
	AppendUser(ctx context.Context, text string) error
	// AppendUserContent appends one user message with arbitrary content
	// blocks (text + images). Used by RunTurnWithImages.
	AppendUserContent(ctx context.Context, blocks []common.ContentBlock) error
	AppendAssistant(ctx context.Context, msg common.Message) error
	AppendToolResults(ctx context.Context, results []ToolResult) error
}

// ModelPort is a stateless model call: given the full message history, system
// reminders, and the tool definitions for this turn, it returns tool calls to
// execute or a final answer. Adapters implement it; the core loop depends
// only on this interface.
type ModelPort interface {
	DoReasoning(ctx context.Context, messages []common.Message, systemReminders []string, tools []common.ToolDefinition) (ReasoningResult, error)
}

// ToolPort owns tool definitions and execution. BeginTurn is called once by
// the loop at turn start so composite ports can snapshot dynamic sources.
// ReadOnly reports whether the named tool performs no mutations — the loop
// runs consecutive read-only calls concurrently; anything unknown or
// unmarked must report false.
type ToolPort interface {
	BeginTurn(ctx context.Context)
	Definitions() []common.ToolDefinition
	Origin(name string) string
	ReadOnly(name string) bool
	Execute(ctx context.Context, call ToolCall) ToolResult
}
