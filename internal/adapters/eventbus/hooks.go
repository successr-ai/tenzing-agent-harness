package eventbus

import "github.com/successr-ai/tenzing-agent-harness/internal/core"

// Hooks holds optional typed callback functions, one per event type.
// Set only the hooks you care about; nil hooks are silently skipped.
type Hooks struct {
	OnSessionStarted        func(core.SessionStartedEvent)
	OnSessionEnded          func(core.SessionEndedEvent)
	OnTurnStarted           func(core.TurnStartedEvent)
	OnTurnCompleted         func(core.TurnCompletedEvent)
	OnLoopStarted           func(core.LoopStartedEvent)
	OnLoopStopped           func(core.LoopStoppedEvent)
	OnReasoningStarted      func(core.ReasoningStartedEvent)
	OnReasoningFinished     func(core.ReasoningFinishedEvent)
	OnToolExecutionStarted  func(core.ToolExecutionStartedEvent)
	OnToolExecutionFinished func(core.ToolExecutionFinishedEvent)
	OnLLMResponse           func(core.LLMResponseEvent)
	OnToolSucceeded         func(core.ToolSucceededEvent)
	OnToolFailed            func(core.ToolFailedEvent)
	OnToolDenied            func(core.ToolDeniedEvent)
	OnToolProgress          func(core.ToolProgressEvent)
	OnContextCompressing    func(core.ContextCompressingEvent)
	OnContextCompressed     func(core.ContextCompressedEvent)
	OnError                 func(core.ErrorEvent)
	OnSubagentStarted       func(core.SubagentStartedEvent)
	OnSubagentStopped       func(core.SubagentStoppedEvent)
	OnTaskCreated           func(core.TaskCreatedEvent)
	OnTaskCompleted         func(core.TaskCompletedEvent)
	OnApprovalRequested     func(core.ApprovalRequestedEvent)
	OnSteeringInjected      func(core.SteeringInjectedEvent)
	OnLLMRetry              func(core.LLMRetryEvent)
	OnModelChanged          func(core.ModelChangedEvent)
	OnThinkingChanged       func(core.ThinkingChangedEvent)
	OnImagesAttached        func(core.ImagesAttachedEvent)
}

// StartHooks subscribes to bus with a buffer of 64 and dispatches each
// received event to the matching typed hook function. The returned stop
// function unsubscribes, which closes the channel and ends the dispatch
// goroutine; the goroutine also ends when the bus itself is closed. stop is
// safe to call after the bus is closed.
func StartHooks(bus *EventBus, hooks Hooks) (stop func()) {
	ch := bus.Subscribe(64)
	go func() {
		for ev := range ch {
			dispatch(ev, hooks)
		}
	}()
	return func() { bus.Unsubscribe(ch) }
}

func dispatch(ev core.Event, h Hooks) {
	switch e := ev.(type) {
	case core.SessionStartedEvent:
		if h.OnSessionStarted != nil {
			h.OnSessionStarted(e)
		}
	case core.SessionEndedEvent:
		if h.OnSessionEnded != nil {
			h.OnSessionEnded(e)
		}
	case core.TurnStartedEvent:
		if h.OnTurnStarted != nil {
			h.OnTurnStarted(e)
		}
	case core.TurnCompletedEvent:
		if h.OnTurnCompleted != nil {
			h.OnTurnCompleted(e)
		}
	case core.LoopStartedEvent:
		if h.OnLoopStarted != nil {
			h.OnLoopStarted(e)
		}
	case core.LoopStoppedEvent:
		if h.OnLoopStopped != nil {
			h.OnLoopStopped(e)
		}
	case core.ReasoningStartedEvent:
		if h.OnReasoningStarted != nil {
			h.OnReasoningStarted(e)
		}
	case core.ReasoningFinishedEvent:
		if h.OnReasoningFinished != nil {
			h.OnReasoningFinished(e)
		}
	case core.ToolExecutionStartedEvent:
		if h.OnToolExecutionStarted != nil {
			h.OnToolExecutionStarted(e)
		}
	case core.ToolExecutionFinishedEvent:
		if h.OnToolExecutionFinished != nil {
			h.OnToolExecutionFinished(e)
		}
	case core.LLMResponseEvent:
		if h.OnLLMResponse != nil {
			h.OnLLMResponse(e)
		}
	case core.ToolSucceededEvent:
		if h.OnToolSucceeded != nil {
			h.OnToolSucceeded(e)
		}
	case core.ToolFailedEvent:
		if h.OnToolFailed != nil {
			h.OnToolFailed(e)
		}
	case core.ToolDeniedEvent:
		if h.OnToolDenied != nil {
			h.OnToolDenied(e)
		}
	case core.ToolProgressEvent:
		if h.OnToolProgress != nil {
			h.OnToolProgress(e)
		}
	case core.ContextCompressingEvent:
		if h.OnContextCompressing != nil {
			h.OnContextCompressing(e)
		}
	case core.ContextCompressedEvent:
		if h.OnContextCompressed != nil {
			h.OnContextCompressed(e)
		}
	case core.ErrorEvent:
		if h.OnError != nil {
			h.OnError(e)
		}
	case core.SubagentStartedEvent:
		if h.OnSubagentStarted != nil {
			h.OnSubagentStarted(e)
		}
	case core.SubagentStoppedEvent:
		if h.OnSubagentStopped != nil {
			h.OnSubagentStopped(e)
		}
	case core.TaskCreatedEvent:
		if h.OnTaskCreated != nil {
			h.OnTaskCreated(e)
		}
	case core.TaskCompletedEvent:
		if h.OnTaskCompleted != nil {
			h.OnTaskCompleted(e)
		}
	case core.ApprovalRequestedEvent:
		if h.OnApprovalRequested != nil {
			h.OnApprovalRequested(e)
		}
	case core.SteeringInjectedEvent:
		if h.OnSteeringInjected != nil {
			h.OnSteeringInjected(e)
		}
	case core.LLMRetryEvent:
		if h.OnLLMRetry != nil {
			h.OnLLMRetry(e)
		}
	case core.ModelChangedEvent:
		if h.OnModelChanged != nil {
			h.OnModelChanged(e)
		}
	case core.ThinkingChangedEvent:
		if h.OnThinkingChanged != nil {
			h.OnThinkingChanged(e)
		}
	case core.ImagesAttachedEvent:
		if h.OnImagesAttached != nil {
			h.OnImagesAttached(e)
		}
	}
}
