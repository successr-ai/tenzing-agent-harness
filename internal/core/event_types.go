package core

import "time"

// --- Session-level ---

type SessionStartedEvent struct {
	BaseEvent
}

type SessionEndedEvent struct {
	BaseEvent
	TurnCount int           `json:"turn_count"`
	Duration  time.Duration `json:"duration_ms"`
}

// --- Turn-level ---

type TurnStartedEvent struct {
	BaseEvent
	Query string `json:"query"`
}

type TurnCompletedEvent struct {
	BaseEvent
	FinalAnswer string        `json:"final_answer"`
	Iterations  int           `json:"iterations"`
	Duration    time.Duration `json:"duration_ms"`
}

// --- FSM-level ---

type LoopStartedEvent struct {
	BaseEvent
	Input string `json:"input"`
}

type LoopStoppedEvent struct {
	BaseEvent
	Iterations int           `json:"iterations"`
	Duration   time.Duration `json:"duration_ms"`
}

type ReasoningStartedEvent struct {
	BaseEvent
	Iteration int `json:"iteration"`
}

type ReasoningFinishedEvent struct {
	BaseEvent
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	StopReason   string `json:"stop_reason"`
	HasToolCall  bool   `json:"has_tool_call"`
}

type ToolExecutionStartedEvent struct {
	BaseEvent
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
}

type ToolExecutionFinishedEvent struct {
	BaseEvent
	ToolName string        `json:"tool_name"`
	Duration time.Duration `json:"duration_ms"`
}

// --- Business-level ---

type LLMResponseEvent struct {
	BaseEvent
	Model                    string `json:"model"`
	ResponseID               string `json:"response_id"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens,omitempty"`
	StopReason               string `json:"stop_reason"`
	Text                     string `json:"text"`
}

type ToolSucceededEvent struct {
	BaseEvent
	ToolName string        `json:"tool_name"`
	Input    string        `json:"input"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration_ms"`
	// Metadata carries the tool result's side-channel data — content meant
	// for a driver rather than the model, such as Edit/Write's unified diff.
	Metadata map[string]string `json:"metadata,omitempty"`
}

type ToolFailedEvent struct {
	BaseEvent
	ToolName string        `json:"tool_name"`
	Input    string        `json:"input"`
	Error    string        `json:"error"`
	Duration time.Duration `json:"duration_ms"`
}

// ToolDeniedEvent fires when a tool call is blocked by the permission
// system — a policy Deny, or an AskUser escalation that was not approved
// (user denial, timeout, cancellation, or no approver configured). The call
// also produces a ToolFailedEvent; this event is the typed signal that the
// failure was a permission denial, not a tool error.
type ToolDeniedEvent struct {
	BaseEvent
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Reason   string `json:"reason"`
}

type ToolProgressEvent struct {
	BaseEvent
	ToolName  string `json:"tool_name"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	Iteration int    `json:"iteration,omitempty"`
	TokensIn  int64  `json:"tokens_in,omitempty"`
	TokensOut int64  `json:"tokens_out,omitempty"`
}

type ContextCompressingEvent struct {
	BaseEvent
	MessageCount int `json:"message_count"`
}

type ContextCompressedEvent struct {
	BaseEvent
	MessagesBefore int    `json:"messages_before"`
	MessagesAfter  int    `json:"messages_after"`
	Summary        string `json:"summary"`
}

type ErrorEvent struct {
	BaseEvent
	Error   string `json:"error"`
	Context string `json:"context"`
}

// SteeringInjectedEvent fires when a user message submitted mid-turn is
// injected into the loop at a tool-execution boundary.
type SteeringInjectedEvent struct {
	BaseEvent
	Message string `json:"message"`
}

// LLMRetryEvent fires before each transient-LLM-error retry sleep.
type LLMRetryEvent struct {
	BaseEvent
	Attempt    int           `json:"attempt"`
	MaxRetries int           `json:"max_retries"`
	Error      string        `json:"error"`
	Delay      time.Duration `json:"delay_ms"`
}

// ModelChangedEvent fires when the main agent's model is switched between
// turns via Harness.SetModel.
type ModelChangedEvent struct {
	BaseEvent
	From string `json:"from"`
	To   string `json:"to"`
}

// ThinkingChangedEvent fires when model reasoning is toggled between turns
// via Harness.SetThinking.
type ThinkingChangedEvent struct {
	BaseEvent
	Enabled bool `json:"enabled"`
}

// ImageData is one attached image: media type + raw base64 data.
type ImageData struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ImagesAttachedEvent fires before a turn starts when the query carries
// images. The session persister writes them as sidecar blobs; the payload
// carries full base64 data, so consumers that only log should not dump it.
type ImagesAttachedEvent struct {
	BaseEvent
	Images []ImageData `json:"images"`
}

// --- Subagent lifecycle ---

type SubagentStartedEvent struct {
	BaseEvent
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	Prompt    string `json:"prompt"`
}

type SubagentStoppedEvent struct {
	BaseEvent
	AgentID    string        `json:"agent_id"`
	AgentType  string        `json:"agent_type"`
	Iterations int           `json:"iterations"`
	Duration   time.Duration `json:"duration_ms"`
}

// --- Task lifecycle ---

type TaskCreatedEvent struct {
	BaseEvent
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
}

type TaskCompletedEvent struct {
	BaseEvent
	TaskID string `json:"task_id"`
}
