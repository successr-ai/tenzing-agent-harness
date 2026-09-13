package common

import (
	"encoding/json"
	"strings"
)

// Role represents the author of a message in a conversation.
// Both Anthropic and OpenAI use the same role strings, so this type
// normalizes them into a single set of constants.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ContentType identifies what kind of data a ContentBlock carries.
// This is a provider-agnostic discriminator — each provider maps
// its own block types (e.g. Anthropic's "tool_use" blocks, OpenAI's
// tool call objects) into these common variants.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeThinking   ContentType = "thinking"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeImage      ContentType = "image"
)

// ImageSource carries image data for a ContentTypeImage block.
type ImageSource struct {
	// MediaType is the image MIME type, e.g. "image/png" or "image/jpeg".
	MediaType string
	// Data is the raw base64-encoded image bytes — no "data:" URI prefix;
	// providers that need one (OpenAI image_url) add it themselves.
	Data string
}

// ContentBlock is a tagged union representing one piece of content
// within a message. The Type field determines which other fields are
// populated:
//   - ContentTypeText: Text
//   - ContentTypeThinking: Text (model chain-of-thought, never answer text)
//   - ContentTypeToolUse: ToolUseID, ToolName, ToolInput
//   - ContentTypeToolResult: ToolResultID, ToolOutput, ToolName
//   - ContentTypeImage: Image
//
// This structure mirrors the content block concept shared by both
// Anthropic (ContentBlockUnion) and OpenAI (message content + tool calls),
// giving callers a single type to work with regardless of provider.
type ContentBlock struct {
	Type ContentType
	Text string

	// Tool use fields (populated when Type == ContentTypeToolUse).
	// These represent the model requesting a tool invocation.
	ToolUseID string
	ToolName  string
	ToolInput json.RawMessage

	// Tool result fields (populated when Type == ContentTypeToolResult).
	// These represent the caller's response to a tool invocation.
	ToolResultID string
	ToolOutput   string

	// Image is the image payload (populated when Type == ContentTypeImage).
	Image *ImageSource
}

// NewTextContent creates a text content block.
func NewTextContent(text string) ContentBlock {
	return ContentBlock{
		Type: ContentTypeText,
		Text: text,
	}
}

// NewThinkingContent creates a thinking content block carrying the model's
// chain-of-thought. Providers classify reasoning output here so Text() and
// CombinedText() only ever see answer text; consumers that want the raw
// reasoning read these blocks (or CompletionResponse.Thinking).
func NewThinkingContent(text string) ContentBlock {
	return ContentBlock{
		Type: ContentTypeThinking,
		Text: text,
	}
}

// NewToolUseContent creates a content block representing a tool invocation
// from the model. The id ties this call to the corresponding tool result.
func NewToolUseContent(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{
		Type:      ContentTypeToolUse,
		ToolUseID: id,
		ToolName:  name,
		ToolInput: input,
	}
}

// NewToolResultContent creates a content block containing the output of a
// tool invocation. toolUseID must match the ToolUseID from the corresponding
// tool use block so the provider can correlate the result. name is the tool
// that produced the output — providers without call-id linkage (Ollama's
// native API) use it to label the result for the model.
func NewToolResultContent(toolUseID, name, output string) ContentBlock {
	return ContentBlock{
		Type:         ContentTypeToolResult,
		ToolResultID: toolUseID,
		ToolName:     name,
		ToolOutput:   output,
	}
}

// NewImageContent creates an image content block. mediaType is the image
// MIME type (e.g. "image/png"); data is the raw base64-encoded bytes without
// a "data:" URI prefix. Image blocks are meaningful in user messages on
// vision-capable models (see ModelDefinition.SupportsVision); providers
// without image support drop them from the outgoing request.
func NewImageContent(mediaType, data string) ContentBlock {
	return ContentBlock{
		Type:  ContentTypeImage,
		Image: &ImageSource{MediaType: mediaType, Data: data},
	}
}

// Message represents a single turn in a conversation. It pairs a Role
// with one or more ContentBlocks, allowing multi-part messages (e.g. text
// followed by a tool result). This maps to Anthropic's MessageParam and
// OpenAI's ChatCompletionMessageParamUnion.
type Message struct {
	Role    Role
	Content []ContentBlock
}

// NewUserMessage creates a user message with a single text block.
func NewUserMessage(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// NewAssistantMessage creates an assistant message with a single text block.
func NewAssistantMessage(text string) Message {
	return Message{
		Role:    RoleAssistant,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// NewSystemMessage creates a system message with a single text block.
// Note: Anthropic handles system prompts via a dedicated parameter rather
// than as a message, so the provider implementations convert these
// accordingly.
func NewSystemMessage(text string) Message {
	return Message{
		Role:    RoleSystem,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

// StopReason indicates why the model stopped generating tokens.
// Providers use different strings (Anthropic: "end_turn", OpenAI: "stop"),
// so both are represented here and provider implementations map to the
// appropriate constant.
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonMaxTokens StopReason = "max_tokens"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonStop      StopReason = "stop"
)

// Usage captures token consumption for a completion request.
// Both Anthropic and OpenAI report these counts, though they use
// different field names (InputTokens/OutputTokens vs PromptTokens/CompletionTokens).
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	// CacheReadInputTokens and CacheCreationInputTokens report prompt-cache
	// activity. Anthropic only; 0 elsewhere.
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
}

// CompletionRequest is the provider-agnostic input to any LLM completion call.
// Provider implementations convert this into their SDK-specific request params
// (Anthropic's MessageNewParams, OpenAI's ChatCompletionNewParams).
type CompletionRequest struct {
	Model       string
	Messages    []Message
	System      string
	MaxTokens   int64
	Temperature *float64
	Tools       []ToolDefinition
	// Think controls model reasoning (chain-of-thought) for providers that
	// support it. nil uses the model's default — thinking models think, others
	// are unaffected. Explicit true forces it (providers may reject it for
	// models without thinking support); explicit false disables it. Providers
	// without a native switch ignore the field.
	Think *bool
	// ThinkingBudget is the reasoning token budget, honored alongside Think:
	// a budget implies thinking unless Think is explicitly false, which
	// leaves it inert. Anthropic: extended-thinking budget_tokens (minimum
	// 1024, values below are clamped up; must be < MaxTokens). OpenAI: mapped
	// lossily to reasoning_effort (<4096 → low, <16384 → medium, else high).
	// Ollama: mapped to a think level with the same cutoffs. A budget beats a
	// client's configured reasoning effort.
	ThinkingBudget *int64
	// CacheSystemAndTools asks the provider to cache the system prompt and tool
	// definitions across calls. Anthropic honors it (cache_control ephemeral
	// breakpoints); other providers ignore it.
	CacheSystemAndTools bool
}

// CompletionResponse is the provider-agnostic output from an LLM completion call.
// Provider implementations convert their SDK-specific responses into this type,
// normalizing content blocks, stop reasons, and usage across providers.
type CompletionResponse struct {
	ID         string
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
	Model      string
}

// Text returns the text from the first text content block in the response,
// or an empty string if there are no text blocks.
func (r CompletionResponse) Text() string {
	for _, block := range r.Content {
		if block.Type == ContentTypeText {
			return block.Text
		}
	}
	return ""
}

// Thinking returns the model's chain-of-thought: all thinking content blocks
// concatenated in order. Empty when the model did not think (or the provider
// could not separate reasoning from answer text).
func (r CompletionResponse) Thinking() string {
	var sb strings.Builder
	for _, block := range r.Content {
		if block.Type == ContentTypeThinking {
			sb.WriteString(block.Text)
		}
	}
	return sb.String()
}

// ToolCalls returns all tool use content blocks from the response.
// Returns nil if the model did not invoke any tools.
func (r CompletionResponse) ToolCalls() []ContentBlock {
	var calls []ContentBlock
	for _, block := range r.Content {
		if block.Type == ContentTypeToolUse {
			calls = append(calls, block)
		}
	}
	return calls
}

// StreamEventType identifies the kind of event received during a streaming
// completion. Events flow in order: Start -> Delta(s) -> Stop, with Error
// possible at any point.
type StreamEventType string

const (
	StreamEventStart    StreamEventType = "start"
	StreamEventDelta    StreamEventType = "delta"
	StreamEventThinking StreamEventType = "thinking"
	StreamEventStop     StreamEventType = "stop"
	StreamEventError    StreamEventType = "error"
)

// StreamEvent represents a single event in a streaming completion response.
// Callers receive these on the channel passed to SendStreamingMessage.
// The Type field determines which other fields are populated.
type StreamEvent struct {
	Type StreamEventType
	// Text contains the incremental text delta (populated for StreamEventDelta).
	Text string
	// Response contains the final accumulated response (populated for StreamEventStop).
	Response *CompletionResponse
	// Err contains error details (populated for StreamEventError).
	Err error
}

// ToolDefinition describes a tool/function that the model can invoke.
// This maps to Anthropic's ToolParam and OpenAI's FunctionDefinitionParam,
// providing a single way to define tools regardless of provider.
type ToolDefinition struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object describing the tool's parameters.
	InputSchema json.RawMessage
}

// TokenCount holds the result of a token counting operation.
// Anthropic provides this via a dedicated API endpoint; OpenAI does not
// have an equivalent, so the OpenAI implementation returns an estimate.
type TokenCount struct {
	InputTokens int64
}

// ModelInfo describes an available model returned by ListModels.
// The fields are the common subset reported by both Anthropic and OpenAI.
type ModelInfo struct {
	ID        string
	Name      string
	MaxTokens int64
}
