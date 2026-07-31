package core

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// An Agent is an abstraction of the "brain" of the harness. Any decision making for the harness
// is executed from an Agent. Agents are stateless: DoReasoning receives the full message history
// on every call — the runner owns the conversation via a ContextPort.
type Agent interface {
	GetCurrentModel() string
	UpdateStreamCallback(fn func(text string))
	UpdateThinkingCallback(fn func(text string))
	DoReasoning(ctx context.Context, messages []common.Message, systemReminders []string, tools []common.ToolDefinition) (ReasoningResult, error)
}
