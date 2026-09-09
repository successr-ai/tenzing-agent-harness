package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
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
		wantJSON  string // marshaled sseEnvelope
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
			event, payload, ok := translateSSE(tt.ev, subagents)
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

// --- serve-mode test harness ---

// lastMessageText extracts the text of the newest message in the history —
// the query the current turn was started with.
func lastMessageText(messages []common.Message) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range messages[len(messages)-1].Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// gatedAgent blocks each turn until released, recording the query it saw.
// One DoReasoning call per turn (always a final answer).
type gatedAgent struct {
	mu      sync.Mutex
	queries []string
	gate    chan struct{}
}

func (a *gatedAgent) GetCurrentModel() string               { return "gated" }
func (a *gatedAgent) UpdateStreamCallback(_ func(string))   {}
func (a *gatedAgent) UpdateThinkingCallback(_ func(string)) {}

func (a *gatedAgent) DoReasoning(ctx context.Context, messages []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	a.mu.Lock()
	a.queries = append(a.queries, lastMessageText(messages))
	a.mu.Unlock()
	select {
	case <-a.gate:
	case <-ctx.Done():
		return core.ReasoningResult{}, ctx.Err()
	}
	return core.ReasoningResult{FinalAnswer: "ok"}, nil
}

func (a *gatedAgent) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]string, len(a.queries))
	copy(cp, a.queries)
	return cp
}

func newTestServer(t *testing.T, agent core.Agent, extraOpts ...harness.HarnessOption) *agentServer {
	t.Helper()
	bus := eventbus.NewEventBus()
	reg := testRegistry(t, config.ModelEntry{Name: "test-model", Provider: "local", ModelName: "glm-5.3"})
	model, err := reg.resolve("test-model")
	if err != nil {
		t.Fatalf("resolve test model: %v", err)
	}
	api, err := newAgentServer(
		model,
		bus, nil, app.NewLogBroadcaster(), nil, nil,
		append([]harness.HarnessOption{
			harness.WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
			harness.WithSubagentDepth(0),
			// keep session files out of the real user config dir
			harness.WithSessionDir(t.TempDir()),
		}, extraOpts...)...,
	)
	if err != nil {
		t.Fatalf("newAgentServer: %v", err)
	}
	t.Cleanup(func() {
		api.cancelActiveTurn()
		api.harness.Shutdown()
		bus.Close()
	})
	return api
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestQueryQueuesWhileBusyAndDrainsInOrder(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	api := newTestServer(t, agent)

	if got := api.startTurnOrQueue(turnRequest{query: "q1"}); got != "started" {
		t.Fatalf("first query status = %q, want started", got)
	}
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })

	if got := api.startTurnOrQueue(turnRequest{query: "q2"}); got != "queued" {
		t.Fatalf("second query status = %q, want queued", got)
	}
	if got := api.startTurnOrQueue(turnRequest{query: "q3"}); got != "queued" {
		t.Fatalf("third query status = %q, want queued", got)
	}

	agent.gate <- struct{}{} // finish q1
	waitFor(t, "q2 to start from the queue", func() bool { return len(agent.seen()) == 2 })
	agent.gate <- struct{}{} // finish q2
	waitFor(t, "q3 to start from the queue", func() bool { return len(agent.seen()) == 3 })
	agent.gate <- struct{}{} // finish q3

	waitFor(t, "server to go idle", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return api.cancelFn == nil && len(api.queue) == 0
	})

	got := agent.seen()
	want := []string{"q1", "q2", "q3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("processed order = %v, want %v", got, want)
		}
	}
}

func TestCancelDropsQueue(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	api := newTestServer(t, agent)

	if got := api.startTurnOrQueue(turnRequest{query: "q1"}); got != "started" {
		t.Fatalf("first query status = %q, want started", got)
	}
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })
	if got := api.startTurnOrQueue(turnRequest{query: "q2"}); got != "queued" {
		t.Fatalf("second query status = %q, want queued", got)
	}

	out, err := api.handleCancel(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleCancel: %v", err)
	}
	if !strings.Contains(out.Body.Status, "cancelled") {
		t.Fatalf("cancel status = %q, want cancelled", out.Body.Status)
	}

	waitFor(t, "server to go idle after cancel", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return api.cancelFn == nil
	})
	// q2 must never run: the cancel dropped it before q1 finished.
	if got := agent.seen(); len(got) != 1 {
		t.Fatalf("queries after cancel = %v, want only q1", got)
	}
}

func TestStateEndpoint(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	api := newTestServer(t, agent)

	out, err := api.handleState(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleState: %v", err)
	}
	if out.Body.State != "idle" {
		t.Fatalf("state = %q, want idle", out.Body.State)
	}
	if out.Body.ConversationID == "" || out.Body.Model == "" || out.Body.Tools == 0 {
		t.Fatalf("state missing metadata: %+v", out.Body)
	}
	if out.Body.Vision {
		t.Fatalf("vision = true for non-vision test model")
	}

	if got := api.startTurnOrQueue(turnRequest{query: "q1"}); got != "started" {
		t.Fatalf("query status = %q, want started", got)
	}
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })
	api.startTurnOrQueue(turnRequest{query: "q2"})

	out, err = api.handleState(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleState while running: %v", err)
	}
	if out.Body.State != "running" {
		t.Fatalf("state = %q, want running", out.Body.State)
	}
	if out.Body.Queued != 1 {
		t.Fatalf("queued = %d, want 1", out.Body.Queued)
	}
	agent.gate <- struct{}{}
	agent.gate <- struct{}{}
}

// TestHandleIndexDebugFlag proves the index page carries the server's
// --debug state as the DEBUG constant the UI gates verbose thinking and
// tool output on, and that no placeholder survives.
func TestHandleIndexDebugFlag(t *testing.T) {
	for _, debug := range []bool{false, true} {
		s := &agentServer{debug: debug}
		rec := httptest.NewRecorder()
		s.handleIndex(rec, httptest.NewRequest("GET", "/", nil))

		body := rec.Body.String()
		want := "const DEBUG = " + strconv.FormatBool(debug) + ";"
		if !strings.Contains(body, want) {
			t.Errorf("debug=%v: index missing %q", debug, want)
		}
		if strings.Contains(body, "__DEBUG__") {
			t.Errorf("debug=%v: placeholder not substituted", debug)
		}
	}
}

// TestContextWindow proves the status gauge's denominator prefers the
// window the model actually runs at over its architectural maximum, and
// reports 0 (gauge hidden) when neither is known.
func TestContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		model common.Model
		want  int
	}{
		{"default window wins over maximum", common.ModelDefinition{ContextWindowSize: 200000, DefaultContextWindow: 32768}, 32768},
		{"maximum used when no default", common.ModelDefinition{ContextWindowSize: 200000}, 200000},
		{"unknown when neither set", common.ModelDefinition{}, 0},
		{"unknown when no model", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextWindow(tt.model); got != tt.want {
				t.Errorf("contextWindow() = %d, want %d", got, tt.want)
			}
		})
	}
}
