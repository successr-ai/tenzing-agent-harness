package sse

import (
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wire"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Envelope is one SSE payload for a harness event: the wire envelope plus
// the server-side agent label (blackboard slot name) for events from
// subagent runners. The SSE event name is the envelope's Type.
type Envelope struct {
	wire.Envelope
	// Agent is the blackboard slot name ("a1", ...) of the subagent that
	// produced the event; empty for the main agent.
	Agent string `json:"agent,omitempty"`
}

// Translate maps one bus event to its SSE message via the wire schema.
// subagents maps runner IDs to blackboard slot names; ok=false means the
// event type is not forwarded to clients.
func Translate(ev core.Event, subagents map[string]string) (event string, payload Envelope, ok bool) {
	var agent string
	switch e := ev.(type) {
	case core.ToolExecutionStartedEvent:
		agent = subagents[e.RunnerID]
	case core.ToolSucceededEvent:
		agent = subagents[e.RunnerID]
	case core.ToolFailedEvent:
		agent = subagents[e.RunnerID]
	case core.ApprovalRequestedEvent:
		agent = subagents[e.RunnerID]
	case core.SubagentStartedEvent, core.SubagentStoppedEvent,
		core.LLMResponseEvent, core.ToolProgressEvent,
		core.SteeringInjectedEvent, core.LLMRetryEvent,
		core.ModelChangedEvent, core.ThinkingChangedEvent,
		core.ImagesAttachedEvent, core.SystemOneDecisionEvent,
		nexus.ChannelErrorEvent, nexus.ChannelStatusEvent, nexus.TriggerEvent:
		// forwarded without an agent label
	default:
		return "", Envelope{}, false
	}
	env := wire.ToWire(ev)
	return env.Type, Envelope{Envelope: env, Agent: agent}, true
}
