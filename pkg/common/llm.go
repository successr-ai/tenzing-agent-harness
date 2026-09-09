package common

import (
	"context"
	"errors"
)

var ErrUnknownProvider = errors.New("unknown provider")

var ErrNotSupported = errors.New("operation not supported by this provider")

type LLM interface {
	// SendSyncMessage sends a completion request and returns the full response.
	SendSyncMessage(ctx context.Context, req CompletionRequest) (CompletionResponse, error)

	// SendStreamingMessage sends a completion request and streams response events
	// to the provided channel. The channel is closed when the stream is complete.
	SendStreamingMessage(ctx context.Context, req CompletionRequest, events chan<- StreamEvent) error

	// SendMessageWithTools sends a completion request with tool definitions.
	// Returns tool calls in the response content when the model invokes tools.
	SendMessageWithTools(ctx context.Context, req CompletionRequest, tools []ToolDefinition) (CompletionResponse, error)

	// CountTokens estimates token count for the given request without executing it.
	CountTokens(ctx context.Context, req CompletionRequest) (TokenCount, error)

	// ListModels returns the models available from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Gets the current model.
	GetCurrentModel() string

	// GetContextWindowSize returns the model's total context window in tokens.
	GetContextWindowSize() int

	// GetModel returns the model this client was built with.
	GetModel() Model
}

// Model represents a model available from a provider.
type Model interface {
	// GetName returns the model's name.
	GetName() string

	// GetMaxTokens returns the model's maximum token limit.
	GetMaxTokens() int

	// GetContextWindowSize returns the model's total context window in tokens.
	GetContextWindowSize() int

	// GetDefaultContextWindow returns the context window to run at by default
	// (e.g. Ollama's num_ctx), which may be smaller than the model's maximum.
	GetDefaultContextWindow() int

	// GetSupportsThinking reports whether the model supports reasoning
	// controls. Advisory metadata for callers — providers do not gate on it.
	GetSupportsThinking() bool

	// GetSupportsVision reports whether the model accepts image content
	// blocks. Advisory metadata for callers — providers do not gate on it.
	GetSupportsVision() bool
}

// ModelDefinition represents a model available from a provider.
// Implementations of the Model interface can be used to define models for specific providers.
type ModelDefinition struct {
	Name string
	// MaxTokens is the model's maximum output tokens per request.
	MaxTokens int
	// ContextWindowSize is the model's maximum context window in tokens.
	ContextWindowSize int
	// DefaultContextWindow is the context window to run at by default, for
	// providers that size server-side context per request (e.g. Ollama's
	// num_ctx). Set it below ContextWindowSize to bound memory use; leave it
	// 0 to run at the full ContextWindowSize. Most providers ignore it.
	DefaultContextWindow int
	// Provider names the backend this model belongs to (e.g. "anthropic",
	// "ollama"). Pure metadata: nothing in this repo's library code reads it —
	// callers that resolve models by name (like cmd/app) dispatch on it.
	Provider string
	// SupportsThinking reports whether the model supports reasoning controls.
	// Advisory metadata for callers — providers do not gate on it.
	SupportsThinking bool
	// SupportsVision reports whether the model accepts image content blocks.
	// Advisory metadata for callers — providers do not gate on it.
	SupportsVision bool
	// ReasoningEffort names the provider's reasoning tier for this model
	// ("low", "high", Ollama's "max", ...). Passed through verbatim: the
	// valid set is the provider's, and it validates. Empty leaves the
	// provider's own default in place; providers with no tier concept
	// (Anthropic, which takes a numeric budget) ignore it.
	ReasoningEffort string
}

func (m ModelDefinition) GetName() string {
	return m.Name
}

func (m ModelDefinition) GetMaxTokens() int {
	return m.MaxTokens
}

func (m ModelDefinition) GetContextWindowSize() int {
	if m.ContextWindowSize > 0 {
		return m.ContextWindowSize
	}
	return m.DefaultContextWindow
}

func (m ModelDefinition) GetDefaultContextWindow() int {
	if m.DefaultContextWindow > 0 {
		return m.DefaultContextWindow
	}
	return m.ContextWindowSize
}

func (m ModelDefinition) GetSupportsThinking() bool {
	return m.SupportsThinking
}

func (m ModelDefinition) GetSupportsVision() bool {
	return m.SupportsVision
}
