package sse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// TestTranslateSSE proves the SSE payload for each forwarded event type is
// the wire envelope (event name = wire type) plus the server-side agent
// label, and that unforwarded types are dropped.
func TestTranslateSSE(t *testing.T) {
	base := func(et core.EventType, runnerID string) core.BaseEvent {
		return core.BaseEvent{EventType: et, Time: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC), RunnerID: runnerID}
	}
	subagents := map[string]string{"runner-sub": "a1"}

	tests := []struct {
		name      string
		ev        core.Event
		wantOK    bool
		wantEvent string
		wantJSON  string // marshaled Envelope
	}{
		{
			"tool start from subagent gets label",
			core.ToolExecutionStartedEvent{BaseEvent: base(core.EventToolExecutionStarted, "runner-sub"), ToolName: "bash", Input: "ls"},
			true, "tool_execution.started",
			`{"v":1,"type":"tool_execution.started","ts":"2026-07-25T12:00:00Z","runner_id":"runner-sub","data":{"tool_name":"bash","input":"ls"},"agent":"a1"}`,
		},
		{
			"tool start from main has no label",
			core.ToolExecutionStartedEvent{BaseEvent: base(core.EventToolExecutionStarted, "main"), ToolName: "read", Input: "f"},
			true, "tool_execution.started",
			`{"v":1,"type":"tool_execution.started","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"tool_name":"read","input":"f"}}`,
		},
		{
			"tool failed",
			core.ToolFailedEvent{BaseEvent: base(core.EventToolFailed, "runner-sub"), ToolName: "bash", Input: "x", Error: "boom", Duration: time.Second},
			true, "tool.failed",
			`{"v":1,"type":"tool.failed","ts":"2026-07-25T12:00:00Z","runner_id":"runner-sub","data":{"tool_name":"bash","input":"x","error":"boom","duration_ms":1000},"agent":"a1"}`,
		},
		{
			"llm response",
			core.LLMResponseEvent{BaseEvent: base(core.EventLLMResponse, "main"), Model: "m", InputTokens: 3, OutputTokens: 4},
			true, "llm.response",
			`{"v":1,"type":"llm.response","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"model":"m","response_id":"","input_tokens":3,"output_tokens":4,"stop_reason":"","text":""}}`,
		},
		{
			"approval requested",
			core.ApprovalRequestedEvent{BaseEvent: base(core.EventApprovalRequested, "runner-sub"), CallID: "c1", ToolName: "bash", Input: "rm", Reason: "danger", Respond: func(bool) {}},
			true, "approval.requested",
			`{"v":1,"type":"approval.requested","ts":"2026-07-25T12:00:00Z","runner_id":"runner-sub","data":{"call_id":"c1","tool_name":"bash","input":"rm","reason":"danger"},"agent":"a1"}`,
		},
		{
			"subagent started",
			core.SubagentStartedEvent{BaseEvent: base(core.EventSubagentStarted, "runner-sub"), AgentID: "a1", AgentType: "general", Prompt: "p"},
			true, "subagent.started",
			`{"v":1,"type":"subagent.started","ts":"2026-07-25T12:00:00Z","runner_id":"runner-sub","data":{"agent_id":"a1","agent_type":"general","prompt":"p"}}`,
		},
		{
			"steering injected",
			core.SteeringInjectedEvent{BaseEvent: base(core.EventSteeringInjected, "main"), Message: "focus"},
			true, "steering.injected",
			`{"v":1,"type":"steering.injected","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"message":"focus"}}`,
		},
		{
			"model changed",
			core.ModelChangedEvent{BaseEvent: base(core.EventModelChanged, "main"), From: "a", To: "b"},
			true, "model.changed",
			`{"v":1,"type":"model.changed","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"from":"a","to":"b"}}`,
		},
		{
			"thinking changed",
			core.ThinkingChangedEvent{BaseEvent: base(core.EventThinkingChanged, "main"), Enabled: true},
			true, "thinking.changed",
			`{"v":1,"type":"thinking.changed","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"enabled":true}}`,
		},
		{
			"llm retry",
			core.LLMRetryEvent{BaseEvent: base(core.EventLLMRetry, "main"), Attempt: 1, MaxRetries: 3, Error: "overloaded", Delay: 2 * time.Second},
			true, "llm.retry",
			`{"v":1,"type":"llm.retry","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"attempt":1,"max_retries":3,"error":"overloaded","delay_ms":2000}}`,
		},
		{
			"images attached",
			core.ImagesAttachedEvent{BaseEvent: base(core.EventImagesAttached, "main"), Images: []core.ImageData{{MediaType: "image/png", Data: "eA=="}}},
			true, "images.attached",
			`{"v":1,"type":"images.attached","ts":"2026-07-25T12:00:00Z","runner_id":"main","data":{"count":1,"media_types":["image/png"]}}`,
		},
		{
			"nexus channel error",
			nexus.ChannelErrorEvent{BaseEvent: base(nexus.EventChannelError, "nexus"), Channel: "logs", Text: "oops", Seq: 7},
			true, "nexus.channel_error",
			`{"v":1,"type":"nexus.channel_error","ts":"2026-07-25T12:00:00Z","runner_id":"nexus","data":{"channel":"logs","text":"oops","seq":7}}`,
		},
		{
			"unforwarded type dropped",
			core.TurnStartedEvent{BaseEvent: base(core.EventTurnStarted, "main"), Query: "q"},
			false, "", "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, payload, ok := Translate(tt.ev, subagents)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if event != tt.wantEvent {
				t.Errorf("event = %q, want %q", event, tt.wantEvent)
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tt.wantJSON {
				t.Errorf("\n got  %s\n want %s", b, tt.wantJSON)
			}
		})
	}
}
