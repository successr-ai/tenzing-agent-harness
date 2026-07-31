// Package wire defines the stable JSONL wire schema for harness events —
// the contract consumed by print mode (cmd/app -p --output-format json),
// the serve-mode SSE stream (cmd/app/server.go builds its payloads from
// these envelopes), and, later, RPC mode (PI item 13). Payload structs here
// are deliberately decoupled from internal/core's and internal/app/nexus's
// event structs: those packages can rename fields or tags without breaking
// stream consumers, and durations are converted to real milliseconds
// (core's `duration_ms` tags marshal nanoseconds).
//
// Versioning: every line carries "v". Bump Version on any breaking change
// to an existing line's shape; adding new event types is not breaking.
package wire

import (
	"fmt"
	"reflect"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Version is the current wire-schema version stamped on every line.
const Version = 1

// Envelope is one JSONL line: schema version, event type, and a typed
// payload. TS is omitted on stream-local lines (deltas, result); RunnerID
// is omitted on the result line.
type Envelope struct {
	V        int       `json:"v"`
	Type     string    `json:"type"`
	TS       time.Time `json:"ts,omitzero"`
	RunnerID string    `json:"runner_id,omitempty"`
	Data     any       `json:"data,omitempty"`
}

// --- payloads (wire contract — change shapes only with a Version bump) ---

type sessionEnded struct {
	TurnCount  int   `json:"turn_count"`
	DurationMS int64 `json:"duration_ms"`
}

type turnStarted struct {
	Query string `json:"query"`
}

type turnCompleted struct {
	FinalAnswer string `json:"final_answer"`
	Iterations  int    `json:"iterations"`
	DurationMS  int64  `json:"duration_ms"`
}

type loopStarted struct {
	Input string `json:"input"`
}

type loopStopped struct {
	Iterations int   `json:"iterations"`
	DurationMS int64 `json:"duration_ms"`
}

type reasoningStarted struct {
	Iteration int `json:"iteration"`
}

type reasoningFinished struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	StopReason   string `json:"stop_reason"`
	HasToolCall  bool   `json:"has_tool_call"`
}

type toolExecutionStarted struct {
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
}

type toolExecutionFinished struct {
	ToolName   string `json:"tool_name"`
	DurationMS int64  `json:"duration_ms"`
}

type llmResponse struct {
	Model                    string `json:"model"`
	ResponseID               string `json:"response_id"`
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens,omitempty"`
	StopReason               string `json:"stop_reason"`
	Text                     string `json:"text"`
}

type toolSucceeded struct {
	ToolName   string `json:"tool_name"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
}

type toolFailed struct {
	ToolName   string `json:"tool_name"`
	Input      string `json:"input"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

type toolDenied struct {
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Reason   string `json:"reason"`
}

type toolProgress struct {
	ToolName  string `json:"tool_name"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	Iteration int    `json:"iteration,omitempty"`
	TokensIn  int64  `json:"tokens_in,omitempty"`
	TokensOut int64  `json:"tokens_out,omitempty"`
}

type contextCompressing struct {
	MessageCount int `json:"message_count"`
}

type contextCompressed struct {
	MessagesBefore int    `json:"messages_before"`
	MessagesAfter  int    `json:"messages_after"`
	Summary        string `json:"summary"`
}

type errorPayload struct {
	Error   string `json:"error"`
	Context string `json:"context"`
}

type subagentStarted struct {
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	Prompt    string `json:"prompt"`
}

type subagentStopped struct {
	AgentID    string `json:"agent_id"`
	AgentType  string `json:"agent_type"`
	Iterations int    `json:"iterations"`
	DurationMS int64  `json:"duration_ms"`
}

type taskCreated struct {
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
}

type taskCompleted struct {
	TaskID string `json:"task_id"`
}

type approvalRequested struct {
	CallID   string `json:"call_id"`
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
	Reason   string `json:"reason"`
}

type steeringInjected struct {
	Message string `json:"message"`
}

type llmRetry struct {
	Attempt    int    `json:"attempt"`
	MaxRetries int    `json:"max_retries"`
	Error      string `json:"error"`
	DelayMS    int64  `json:"delay_ms"`
}

type modelChanged struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type thinkingChanged struct {
	Enabled bool `json:"enabled"`
}

// imagesAttached deliberately reports only count + media types — the event's
// base64 payloads would bloat every consumer's stream.
type imagesAttached struct {
	Count      int      `json:"count"`
	MediaTypes []string `json:"media_types"`
}

type channelError struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
	Seq     uint64 `json:"seq"`
}

type channelStatus struct {
	Channel string `json:"channel"`
	State   string `json:"state"`
}

type nexusTrigger struct {
	Channels []string `json:"channels"`
}

type unknown struct {
	EventType string `json:"event_type"`
	GoType    string `json:"go_type"`
}

type textPayload struct {
	Text string `json:"text"`
}

type resultPayload struct {
	Answer      string `json:"answer"`
	IsError     bool   `json:"is_error"`
	Error       string `json:"error,omitempty"`
	DeniedTools int    `json:"denied_tools,omitempty"`
}

// ToWire converts a core event into its wire envelope. Events without an
// explicit mapping become a "unknown_event" line rather than leaking core's
// internal struct shape onto the stream.
func ToWire(ev core.Event) Envelope {
	env := Envelope{V: Version, Type: string(ev.Type()), TS: ev.Timestamp()}
	setBase := func(be core.BaseEvent) { env.RunnerID = be.RunnerID }

	switch e := ev.(type) {
	case core.SessionStartedEvent:
		setBase(e.BaseEvent)
	case core.SessionEndedEvent:
		setBase(e.BaseEvent)
		env.Data = sessionEnded{TurnCount: e.TurnCount, DurationMS: e.Duration.Milliseconds()}
	case core.TurnStartedEvent:
		setBase(e.BaseEvent)
		env.Data = turnStarted{Query: e.Query}
	case core.TurnCompletedEvent:
		setBase(e.BaseEvent)
		env.Data = turnCompleted{FinalAnswer: e.FinalAnswer, Iterations: e.Iterations, DurationMS: e.Duration.Milliseconds()}
	case core.LoopStartedEvent:
		setBase(e.BaseEvent)
		env.Data = loopStarted{Input: e.Input}
	case core.LoopStoppedEvent:
		setBase(e.BaseEvent)
		env.Data = loopStopped{Iterations: e.Iterations, DurationMS: e.Duration.Milliseconds()}
	case core.ReasoningStartedEvent:
		setBase(e.BaseEvent)
		env.Data = reasoningStarted{Iteration: e.Iteration}
	case core.ReasoningFinishedEvent:
		setBase(e.BaseEvent)
		env.Data = reasoningFinished{Model: e.Model, InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, StopReason: e.StopReason, HasToolCall: e.HasToolCall}
	case core.ToolExecutionStartedEvent:
		setBase(e.BaseEvent)
		env.Data = toolExecutionStarted{ToolName: e.ToolName, Input: e.Input}
	case core.ToolExecutionFinishedEvent:
		setBase(e.BaseEvent)
		env.Data = toolExecutionFinished{ToolName: e.ToolName, DurationMS: e.Duration.Milliseconds()}
	case core.LLMResponseEvent:
		setBase(e.BaseEvent)
		env.Data = llmResponse{Model: e.Model, ResponseID: e.ResponseID, InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, CacheReadInputTokens: e.CacheReadInputTokens, CacheCreationInputTokens: e.CacheCreationInputTokens, StopReason: e.StopReason, Text: e.Text}
	case core.ToolSucceededEvent:
		setBase(e.BaseEvent)
		env.Data = toolSucceeded{ToolName: e.ToolName, Input: e.Input, Output: e.Output, DurationMS: e.Duration.Milliseconds()}
	case core.ToolFailedEvent:
		setBase(e.BaseEvent)
		env.Data = toolFailed{ToolName: e.ToolName, Input: e.Input, Error: e.Error, DurationMS: e.Duration.Milliseconds()}
	case core.ToolDeniedEvent:
		setBase(e.BaseEvent)
		env.Data = toolDenied{ToolName: e.ToolName, Input: e.Input, Reason: e.Reason}
	case core.ToolProgressEvent:
		setBase(e.BaseEvent)
		env.Data = toolProgress{ToolName: e.ToolName, Phase: e.Phase, Detail: e.Detail, Iteration: e.Iteration, TokensIn: e.TokensIn, TokensOut: e.TokensOut}
	case core.ContextCompressingEvent:
		setBase(e.BaseEvent)
		env.Data = contextCompressing{MessageCount: e.MessageCount}
	case core.ContextCompressedEvent:
		setBase(e.BaseEvent)
		env.Data = contextCompressed{MessagesBefore: e.MessagesBefore, MessagesAfter: e.MessagesAfter, Summary: e.Summary}
	case core.ErrorEvent:
		setBase(e.BaseEvent)
		env.Data = errorPayload{Error: e.Error, Context: e.Context}
	case core.SubagentStartedEvent:
		setBase(e.BaseEvent)
		env.Data = subagentStarted{AgentID: e.AgentID, AgentType: e.AgentType, Prompt: e.Prompt}
	case core.SubagentStoppedEvent:
		setBase(e.BaseEvent)
		env.Data = subagentStopped{AgentID: e.AgentID, AgentType: e.AgentType, Iterations: e.Iterations, DurationMS: e.Duration.Milliseconds()}
	case core.TaskCreatedEvent:
		setBase(e.BaseEvent)
		env.Data = taskCreated{TaskID: e.TaskID, Description: e.Description}
	case core.TaskCompletedEvent:
		setBase(e.BaseEvent)
		env.Data = taskCompleted{TaskID: e.TaskID}
	case core.ApprovalRequestedEvent:
		setBase(e.BaseEvent)
		env.Data = approvalRequested{CallID: e.CallID, ToolName: e.ToolName, Input: e.Input, Reason: e.Reason}
	case core.SteeringInjectedEvent:
		setBase(e.BaseEvent)
		env.Data = steeringInjected{Message: e.Message}
	case core.LLMRetryEvent:
		setBase(e.BaseEvent)
		env.Data = llmRetry{Attempt: e.Attempt, MaxRetries: e.MaxRetries, Error: e.Error, DelayMS: e.Delay.Milliseconds()}
	case core.ModelChangedEvent:
		setBase(e.BaseEvent)
		env.Data = modelChanged{From: e.From, To: e.To}
	case core.ThinkingChangedEvent:
		setBase(e.BaseEvent)
		env.Data = thinkingChanged{Enabled: e.Enabled}
	case core.ImagesAttachedEvent:
		setBase(e.BaseEvent)
		types := make([]string, len(e.Images))
		for i, img := range e.Images {
			types[i] = img.MediaType
		}
		env.Data = imagesAttached{Count: len(e.Images), MediaTypes: types}
	case nexus.ChannelErrorEvent:
		setBase(e.BaseEvent)
		env.Data = channelError{Channel: e.Channel, Text: e.Text, Seq: e.Seq}
	case nexus.ChannelStatusEvent:
		setBase(e.BaseEvent)
		env.Data = channelStatus{Channel: e.Channel, State: e.State}
	case nexus.TriggerEvent:
		setBase(e.BaseEvent)
		env.Data = nexusTrigger{Channels: e.Channels}
	default:
		env.Type = "unknown_event"
		env.RunnerID = runnerIDOf(ev)
		env.Data = unknown{EventType: string(ev.Type()), GoType: fmt.Sprintf("%T", ev)}
	}
	return env
}

// runnerIDOf recovers the runner id from an unmapped event. core.Event
// exposes no accessor (BaseEvent's RunnerID field blocks a method of the
// same name), so read the promoted field reflectively — every event embeds
// core.BaseEvent.
func runnerIDOf(ev core.Event) string {
	v := reflect.Indirect(reflect.ValueOf(ev))
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName("RunnerID")
	if f.IsValid() && f.Kind() == reflect.String {
		return f.String()
	}
	return ""
}

// TextDelta is one streamed chunk of the model's answer text, tagged with
// the emitting runner's id so multiplexed streams stay correlatable.
func TextDelta(runnerID, text string) Envelope {
	return Envelope{V: Version, Type: "text_delta", RunnerID: runnerID, Data: textPayload{Text: text}}
}

// ThinkingDelta is one streamed chunk of the model's thinking text, tagged
// like TextDelta.
func ThinkingDelta(runnerID, text string) Envelope {
	return Envelope{V: Version, Type: "thinking_delta", RunnerID: runnerID, Data: textPayload{Text: text}}
}

// Result is the final line of a print-mode stream: the turn's answer or
// error, plus how many tool calls the permission policy denied (0 omits the
// field — additive, no Version bump).
func Result(answer string, deniedTools int, turnErr error) Envelope {
	p := resultPayload{Answer: answer, IsError: turnErr != nil, DeniedTools: deniedTools}
	if turnErr != nil {
		p.Error = turnErr.Error()
	}
	return Envelope{V: Version, Type: "result", Data: p}
}
