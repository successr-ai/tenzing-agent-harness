package api

import (
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func TestForwardEvents(t *testing.T) {
	bus := eventbus.NewEventBus()
	t.Cleanup(bus.Close)
	srv := New(ServerConfig{Bus: bus})
	t.Cleanup(srv.events.Close)
	rec, end := streamEvents(t, srv)
	base := func(et core.EventType, runner string) core.BaseEvent {
		return core.BaseEvent{EventType: et, Time: time.Now(), RunnerID: runner}
	}

	// an approval request is captured and forwarded
	bus.Emit(core.ApprovalRequestedEvent{BaseEvent: base(core.EventApprovalRequested, "main"), CallID: "c1", ToolName: "bash", Input: "ls", Respond: func(bool) {}})
	waitFor(t, "approval captured", func() bool { return srv.approvals.Len() == 1 })
	if p, _ := srv.approvals.Get("c1"); p.Tool != "bash" || p.Input != "ls" || p.Respond == nil {
		t.Errorf("captured approval = %+v", p)
	}

	// subagent tool events carry the blackboard label while the subagent lives
	bus.Emit(core.SubagentStartedEvent{BaseEvent: base(core.EventSubagentStarted, "r-sub"), AgentID: "a1"})
	bus.Emit(core.ToolExecutionStartedEvent{BaseEvent: base(core.EventToolExecutionStarted, "r-sub"), ToolName: "read", Input: "f"})
	bus.Emit(core.SubagentStoppedEvent{BaseEvent: base(core.EventSubagentStopped, "r-sub")})
	bus.Emit(core.ToolExecutionStartedEvent{BaseEvent: base(core.EventToolExecutionStarted, "r-sub"), ToolName: "read", Input: "g"})

	// an llm.response is tracked and followed by a cost event
	bus.Emit(core.LLMResponseEvent{BaseEvent: base(core.EventLLMResponse, "main"), Model: "m", InputTokens: 5, OutputTokens: 6})
	waitFor(t, "cost tracked", func() bool { return srv.costs.Stats().Calls == 1 })

	// an unforwarded event type produces nothing on the stream
	bus.Emit(core.ToolDeniedEvent{BaseEvent: base(core.EventToolDenied, "main"), ToolName: "bash"})

	waitFor(t, "cost frame", func() bool { return strings.Contains(rec.String(), "event: cost") })
	end()
	body := rec.String()
	for _, want := range []string{
		"event: approval.requested",
		`"tool_name":"read","input":"f"},"agent":"a1"}`,
		`"tool_name":"read","input":"g"}}`,
		"event: llm.response",
		`event: cost` + "\ndata: {\"input_tokens\":5,\"output_tokens\":6,",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "tool.denied") {
		t.Error("unforwarded event reached the stream")
	}
}
