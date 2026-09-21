package core

import "time"

// EventType identifies the kind of event.
type EventType string

const (
	EventSessionStarted        EventType = "session.started"
	EventSessionEnded          EventType = "session.ended"
	EventTurnStarted           EventType = "turn.started"
	EventTurnCompleted         EventType = "turn.completed"
	EventLoopStarted           EventType = "loop.started"
	EventLoopStopped           EventType = "loop.stopped"
	EventReasoningStarted      EventType = "reasoning.started"
	EventReasoningFinished     EventType = "reasoning.finished"
	EventToolExecutionStarted  EventType = "tool_execution.started"
	EventToolExecutionFinished EventType = "tool_execution.finished"
	EventLLMResponse           EventType = "llm.response"
	EventToolSucceeded         EventType = "tool.succeeded"
	EventToolFailed            EventType = "tool.failed"
	EventToolDenied            EventType = "tool.denied"
	EventToolProgress          EventType = "tool.progress"
	EventContextCompressing    EventType = "context.compressing"
	EventContextCompressed     EventType = "context.compressed"
	EventError                 EventType = "error"
	EventSubagentStarted       EventType = "subagent.started"
	EventSubagentStopped       EventType = "subagent.stopped"
	EventTaskCreated           EventType = "task.created"
	EventTaskCompleted         EventType = "task.completed"
	EventApprovalRequested     EventType = "approval.requested"
	EventSteeringInjected      EventType = "steering.injected"
	EventLLMRetry              EventType = "llm.retry"
	EventModelChanged          EventType = "model.changed"
	EventThinkingChanged       EventType = "thinking.changed"
	EventImagesAttached        EventType = "images.attached"
	EventSystemOneDecision     EventType = "systemone.decision"
)

// Event is the common interface for all harness events.
type Event interface {
	Type() EventType
	Timestamp() time.Time
}

// BaseEvent holds fields common to every event. Embed it in concrete event structs.
type BaseEvent struct {
	EventType EventType `json:"type"`
	Time      time.Time `json:"timestamp"`
	RunnerID  string    `json:"runner_id"`
}

// NewBaseEvent constructs a BaseEvent stamped with the current time.
func NewBaseEvent(eventType EventType, runnerID string) BaseEvent {
	return BaseEvent{
		EventType: eventType,
		Time:      time.Now(),
		RunnerID:  runnerID,
	}
}

func (e BaseEvent) Type() EventType      { return e.EventType }
func (e BaseEvent) Timestamp() time.Time { return e.Time }

// Emitter is the interface for publishing events.
// Implementations may fan out to subscribers, write to a log, etc.
type Emitter interface {
	Emit(event Event)
}
