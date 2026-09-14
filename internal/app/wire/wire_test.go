package wire

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

var testTime = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

func base(t core.EventType) core.BaseEvent {
	return core.BaseEvent{EventType: t, Time: testTime, RunnerID: "r1"}
}

// marshal returns the JSONL line for an envelope, failing the test on error.
func marshal(t *testing.T, env Envelope) string {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestToWireCoversEveryCoreEvent(t *testing.T) {
	const pre = `{"v":1,"type":"`
	const mid = `","ts":"2026-07-24T12:00:00Z","runner_id":"r1"`
	tests := []struct {
		name string
		ev   core.Event
		want string
	}{
		{"session started", core.SessionStartedEvent{BaseEvent: base(core.EventSessionStarted)},
			pre + `session.started` + mid + `}`},
		{"session ended", core.SessionEndedEvent{BaseEvent: base(core.EventSessionEnded), TurnCount: 3, Duration: 90 * time.Second},
			pre + `session.ended` + mid + `,"data":{"turn_count":3,"duration_ms":90000}}`},
		{"turn started", core.TurnStartedEvent{BaseEvent: base(core.EventTurnStarted), Query: "hi"},
			pre + `turn.started` + mid + `,"data":{"query":"hi"}}`},
		{"turn completed", core.TurnCompletedEvent{BaseEvent: base(core.EventTurnCompleted), FinalAnswer: "done", Iterations: 2, Duration: 1500 * time.Millisecond},
			pre + `turn.completed` + mid + `,"data":{"final_answer":"done","iterations":2,"duration_ms":1500}}`},
		{"loop started", core.LoopStartedEvent{BaseEvent: base(core.EventLoopStarted), Input: "in"},
			pre + `loop.started` + mid + `,"data":{"input":"in"}}`},
		{"loop stopped", core.LoopStoppedEvent{BaseEvent: base(core.EventLoopStopped), Iterations: 4, Duration: 2 * time.Second},
			pre + `loop.stopped` + mid + `,"data":{"iterations":4,"duration_ms":2000}}`},
		{"reasoning started", core.ReasoningStartedEvent{BaseEvent: base(core.EventReasoningStarted), Iteration: 1},
			pre + `reasoning.started` + mid + `,"data":{"iteration":1}}`},
		{"reasoning finished", core.ReasoningFinishedEvent{BaseEvent: base(core.EventReasoningFinished), Model: "m", InputTokens: 10, OutputTokens: 5, StopReason: "end_turn", HasToolCall: true},
			pre + `reasoning.finished` + mid + `,"data":{"model":"m","input_tokens":10,"output_tokens":5,"stop_reason":"end_turn","has_tool_call":true}}`},
		{"tool execution started", core.ToolExecutionStartedEvent{BaseEvent: base(core.EventToolExecutionStarted), ToolName: "bash", Input: "ls"},
			pre + `tool_execution.started` + mid + `,"data":{"tool_name":"bash","input":"ls"}}`},
		{"tool execution finished", core.ToolExecutionFinishedEvent{BaseEvent: base(core.EventToolExecutionFinished), ToolName: "bash", Duration: 250 * time.Millisecond},
			pre + `tool_execution.finished` + mid + `,"data":{"tool_name":"bash","duration_ms":250}}`},
		{"llm response", core.LLMResponseEvent{BaseEvent: base(core.EventLLMResponse), Model: "m", ResponseID: "id1", InputTokens: 7, OutputTokens: 8, StopReason: "end_turn", Text: "txt"},
			pre + `llm.response` + mid + `,"data":{"model":"m","response_id":"id1","input_tokens":7,"output_tokens":8,"stop_reason":"end_turn","text":"txt"}}`},
		{"tool succeeded", core.ToolSucceededEvent{BaseEvent: base(core.EventToolSucceeded), ToolName: "read", Input: "f.go", Output: "ok", Duration: 1500 * time.Millisecond},
			pre + `tool.succeeded` + mid + `,"data":{"tool_name":"read","input":"f.go","output":"ok","duration_ms":1500}}`},
		{"tool succeeded with metadata", core.ToolSucceededEvent{BaseEvent: base(core.EventToolSucceeded), ToolName: "Edit", Input: "f.go", Output: "Edit applied. +1 -1", Duration: time.Second, Metadata: map[string]string{"diff_added": "1"}},
			pre + `tool.succeeded` + mid + `,"data":{"tool_name":"Edit","input":"f.go","output":"Edit applied. +1 -1","duration_ms":1000,"metadata":{"diff_added":"1"}}}`},
		{"tool failed", core.ToolFailedEvent{BaseEvent: base(core.EventToolFailed), ToolName: "read", Input: "f.go", Error: "nope", Duration: time.Second},
			pre + `tool.failed` + mid + `,"data":{"tool_name":"read","input":"f.go","error":"nope","duration_ms":1000}}`},
		{"tool denied", core.ToolDeniedEvent{BaseEvent: base(core.EventToolDenied), ToolName: "bash", Input: "rm -rf", Reason: "requires approval"},
			pre + `tool.denied` + mid + `,"data":{"tool_name":"bash","input":"rm -rf","reason":"requires approval"}}`},
		{"tool progress", core.ToolProgressEvent{BaseEvent: base(core.EventToolProgress), ToolName: "spawn_agent", Phase: "run", Detail: "d", Iteration: 2, TokensIn: 3, TokensOut: 4},
			pre + `tool.progress` + mid + `,"data":{"tool_name":"spawn_agent","phase":"run","detail":"d","iteration":2,"tokens_in":3,"tokens_out":4}}`},
		{"context compressing", core.ContextCompressingEvent{BaseEvent: base(core.EventContextCompressing), MessageCount: 40},
			pre + `context.compressing` + mid + `,"data":{"message_count":40}}`},
		{"context compressed", core.ContextCompressedEvent{BaseEvent: base(core.EventContextCompressed), MessagesBefore: 40, MessagesAfter: 6, Summary: "s"},
			pre + `context.compressed` + mid + `,"data":{"messages_before":40,"messages_after":6,"summary":"s"}}`},
		{"error", core.ErrorEvent{BaseEvent: base(core.EventError), Error: "boom", Context: "loop"},
			pre + `error` + mid + `,"data":{"error":"boom","context":"loop"}}`},
		{"subagent started", core.SubagentStartedEvent{BaseEvent: base(core.EventSubagentStarted), AgentID: "a1", AgentType: "general", Prompt: "p"},
			pre + `subagent.started` + mid + `,"data":{"agent_id":"a1","agent_type":"general","prompt":"p"}}`},
		{"subagent stopped", core.SubagentStoppedEvent{BaseEvent: base(core.EventSubagentStopped), AgentID: "a1", AgentType: "general", Iterations: 5, Duration: 3 * time.Second},
			pre + `subagent.stopped` + mid + `,"data":{"agent_id":"a1","agent_type":"general","iterations":5,"duration_ms":3000}}`},
		{"task created", core.TaskCreatedEvent{BaseEvent: base(core.EventTaskCreated), TaskID: "t1", Description: "d"},
			pre + `task.created` + mid + `,"data":{"task_id":"t1","description":"d"}}`},
		{"task completed", core.TaskCompletedEvent{BaseEvent: base(core.EventTaskCompleted), TaskID: "t1"},
			pre + `task.completed` + mid + `,"data":{"task_id":"t1"}}`},
		{"approval requested drops Respond", core.ApprovalRequestedEvent{BaseEvent: base(core.EventApprovalRequested), CallID: "c1", ToolName: "bash", Input: "rm", Reason: "danger", Timeout: 2 * time.Minute, Respond: func(bool) {}},
			pre + `approval.requested` + mid + `,"data":{"call_id":"c1","tool_name":"bash","input":"rm","reason":"danger","timeout_ms":120000}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := marshal(t, ToWire(tt.ev)); got != tt.want {
				t.Errorf("\n got  %s\n want %s", got, tt.want)
			}
		})
	}
}

type unknownEvent struct{ core.BaseEvent }

func TestToWireUnknownEventFallback(t *testing.T) {
	ev := unknownEvent{BaseEvent: base(core.EventType("custom.thing"))}
	got := marshal(t, ToWire(ev))
	// runner_id survives the fallback: it is read from the embedded
	// core.BaseEvent so unmapped events keep runner correlation.
	want := `{"v":1,"type":"unknown_event","ts":"2026-07-24T12:00:00Z","runner_id":"r1","data":{"event_type":"custom.thing","go_type":"wire.unknownEvent"}}`
	if got != want {
		t.Errorf("\n got  %s\n want %s", got, want)
	}
}

func TestStreamLocalLines(t *testing.T) {
	tests := []struct {
		name string
		env  Envelope
		want string
	}{
		{"text delta", TextDelta("r1", "he"), `{"v":1,"type":"text_delta","runner_id":"r1","data":{"text":"he"}}`},
		{"thinking delta", ThinkingDelta("r1", "hm"), `{"v":1,"type":"thinking_delta","runner_id":"r1","data":{"text":"hm"}}`},
		{"result ok", Result("answer", 0, nil), `{"v":1,"type":"result","data":{"answer":"answer","is_error":false}}`},
		{"result error", Result("", 0, errTest), `{"v":1,"type":"result","data":{"answer":"","is_error":true,"error":"turn exploded"}}`},
		{"result with denied tools", Result("partial", 2, nil), `{"v":1,"type":"result","data":{"answer":"partial","is_error":false,"denied_tools":2}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := marshal(t, tt.env); got != tt.want {
				t.Errorf("\n got  %s\n want %s", got, tt.want)
			}
		})
	}
}

type testErr struct{}

func (testErr) Error() string { return "turn exploded" }

var errTest = testErr{}
