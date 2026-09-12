package api

import (
	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/api/sse"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// forwardEvents pumps bus events to SSE clients until ch closes, keeping
// the side effects the pure translation can't own: the subagent label map,
// approval-responder capture, and cost accounting.
func (s *Server) forwardEvents(ch <-chan core.Event) {
	// Sub-agent runners share the bus; map their runner IDs to blackboard
	// slot names ("a1", ...) so tool events can be labeled per agent. Only
	// this goroutine touches the map.
	subagents := make(map[string]string)
	for ev := range ch {
		s.observe(ev, subagents)
		if name, payload, ok := sse.Translate(ev, subagents); ok {
			s.events.Publish(name, payload)
		}
		if _, ok := ev.(core.LLMResponseEvent); ok {
			// Running totals ride behind every llm.response so the UI can
			// show live cost without polling /stats.
			s.events.Publish("cost", s.costs.Stats())
		}
	}
}

// observe applies one event's bookkeeping side effects.
func (s *Server) observe(ev core.Event, subagents map[string]string) {
	switch e := ev.(type) {
	case core.SubagentStartedEvent:
		subagents[e.RunnerID] = e.AgentID
	case core.SubagentStoppedEvent:
		delete(subagents, e.RunnerID)
	case core.ApprovalRequestedEvent:
		s.approvals.Add(e.CallID, approvals.Pending{Respond: e.Respond, Tool: e.ToolName, Input: e.Input})
	case core.LLMResponseEvent:
		s.costs.Track(e)
	}
}
