package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// --- fakes ----------------------------------------------------------------

// fakeModel returns scripted ReasoningResults in sequence.
type fakeModel struct {
	mu    sync.Mutex
	steps []ReasoningResult
	idx   int
}

func (f *fakeModel) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (ReasoningResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.steps) {
		return ReasoningResult{}, fmt.Errorf("fakeModel: no more scripted steps (called %d times, have %d)", f.idx+1, len(f.steps))
	}
	r := f.steps[f.idx]
	f.idx++
	return r, nil
}

func (f *fakeModel) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idx
}

// fakeTools records Execute calls and returns canned results.
type fakeTools struct {
	mu       sync.Mutex
	calls    []ToolCall
	results  map[string]ToolResult // keyed by tool name
	readOnly map[string]bool       // keyed by tool name; nil → all mutating
}

func newFakeTools(results map[string]ToolResult) *fakeTools {
	return &fakeTools{results: results}
}

func (f *fakeTools) BeginTurn(_ context.Context)          {}
func (f *fakeTools) Definitions() []common.ToolDefinition { return nil }
func (f *fakeTools) Origin(name string) string            { return "native" }
func (f *fakeTools) ReadOnly(name string) bool            { return f.readOnly[name] }

func (f *fakeTools) Execute(_ context.Context, call ToolCall) ToolResult {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if r, ok := f.results[call.Name]; ok {
		r.ToolUseID = call.ID
		return r
	}
	return ToolResult{ToolUseID: call.ID, Output: "ok"}
}

func (f *fakeTools) executed() []ToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ToolCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeContext records the call order as []string for assertion.
type fakeContext struct {
	mu          sync.Mutex
	msgs        []common.Message
	callLog     []string
	toolResults [][]ToolResult // one entry per AppendToolResults call
}

func newFakeContext() *fakeContext {
	return &fakeContext{}
}

func (f *fakeContext) Messages(_ context.Context) ([]common.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "Messages")
	out := make([]common.Message, len(f.msgs))
	copy(out, f.msgs)
	return out, nil
}

func (f *fakeContext) AppendUser(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "AppendUser")
	f.msgs = append(f.msgs, common.NewUserMessage(text))
	return nil
}

func (f *fakeContext) AppendUserContent(_ context.Context, blocks []common.ContentBlock) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "AppendUserContent")
	f.msgs = append(f.msgs, common.Message{Role: common.RoleUser, Content: blocks})
	return nil
}

func (f *fakeContext) AppendAssistant(_ context.Context, msg common.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "AppendAssistant")
	f.msgs = append(f.msgs, msg)
	return nil
}

func (f *fakeContext) AppendToolResults(_ context.Context, results []ToolResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLog = append(f.callLog, "AppendToolResults")
	f.toolResults = append(f.toolResults, append([]ToolResult{}, results...))
	blocks := make([]common.ContentBlock, len(results))
	for i, r := range results {
		blocks[i] = common.NewToolResultContent(r.ToolUseID, "", r.Output)
	}
	f.msgs = append(f.msgs, common.Message{Role: common.RoleTool, Content: blocks})
	return nil
}

func (f *fakeContext) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.callLog))
	copy(out, f.callLog)
	return out
}

// fakeEmitter collects events.
type fakeEmitter struct {
	mu     sync.Mutex
	events []Event
}

func (e *fakeEmitter) Emit(ev Event) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

func (e *fakeEmitter) eventTypes() []EventType {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]EventType, len(e.events))
	for i, ev := range e.events {
		out[i] = ev.Type()
	}
	return out
}

// toolCallResult builds a ReasoningResult for a tool_use response.
func toolCallResult(calls ...ToolCall) ReasoningResult {
	blocks := make([]common.ContentBlock, len(calls))
	for i, c := range calls {
		blocks[i] = common.NewToolUseContent(c.ID, c.Name, []byte(c.Input))
	}
	return ReasoningResult{
		ToolCalls: calls,
		Meta:      ResponseMeta{AssistantMessage: common.Message{Role: common.RoleAssistant, Content: blocks}},
	}
}

func newTestLoop(t *testing.T, model ModelPort, tools ToolPort, ctx ContextPort, opts ...func(*LoopConfig)) *Loop {
	t.Helper()
	cfg := LoopConfig{
		ID:      "test-loop",
		Model:   model,
		Tools:   tools,
		Context: ctx,
	}
	for _, o := range opts {
		o(&cfg)
	}
	l, err := NewLoop(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// --- tests ----------------------------------------------------------------

func assertEventOrder(t *testing.T, got, want []EventType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\n  got:  %v\n  want: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q\n  full: %v", i, got[i], want[i], got)
		}
	}
}

func TestRunTurnFinalAnswerImmediately(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		{FinalAnswer: "the answer"},
	}}
	tools := newFakeTools(nil)
	fctx := newFakeContext()
	emitter := &fakeEmitter{}

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) {
		cfg.Emitter = emitter
	})
	tr, err := l.RunTurn(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "the answer" {
		t.Errorf("FinalAnswer = %q, want %q", tr.FinalAnswer, "the answer")
	}
	if tr.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", tr.Iterations)
	}
	if len(tools.executed()) != 0 {
		t.Errorf("expected no tool executions, got %d", len(tools.executed()))
	}

	assertEventOrder(t, emitter.eventTypes(), []EventType{
		EventTurnStarted,
		EventLoopStarted,
		EventReasoningStarted,
		EventReasoningFinished,
		EventLLMResponse,
		EventLoopStopped,
		EventTurnCompleted,
	})
}

func TestRunTurnToolCallThenAnswer(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "search", Input: `{"q":"test"}`}),
		{FinalAnswer: "found it"},
	}}
	tools := newFakeTools(map[string]ToolResult{
		"search": {Output: "result data"},
	})
	fctx := newFakeContext()
	emitter := &fakeEmitter{}

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) {
		cfg.Emitter = emitter
	})
	tr, err := l.RunTurn(context.Background(), "find something")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "found it" {
		t.Errorf("FinalAnswer = %q, want %q", tr.FinalAnswer, "found it")
	}

	// Verify tool was executed
	executed := tools.executed()
	if len(executed) != 1 {
		t.Fatalf("expected 1 tool execution, got %d", len(executed))
	}
	if executed[0].Name != "search" {
		t.Errorf("executed tool = %q, want %q", executed[0].Name, "search")
	}

	// Verify context call order
	callOrder := fctx.calls()
	wantCalls := []string{"AppendUser", "Messages", "AppendAssistant", "AppendToolResults", "Messages", "AppendAssistant"}
	if len(callOrder) != len(wantCalls) {
		t.Fatalf("context calls = %v, want %v", callOrder, wantCalls)
	}
	for i := range wantCalls {
		if callOrder[i] != wantCalls[i] {
			t.Errorf("context call[%d] = %q, want %q", i, callOrder[i], wantCalls[i])
		}
	}

	// Verify event emission order across both iterations
	assertEventOrder(t, emitter.eventTypes(), []EventType{
		// iteration 1: tool call
		EventTurnStarted,
		EventLoopStarted,
		EventReasoningStarted,
		EventReasoningFinished,
		EventLLMResponse,
		EventToolExecutionStarted,
		EventToolExecutionFinished,
		EventToolSucceeded,
		// iteration 2: final answer
		EventReasoningStarted,
		EventReasoningFinished,
		EventLLMResponse,
		EventLoopStopped,
		EventTurnCompleted,
	})
}

// denyExtension denies every tool call.
type denyExtension struct{}

func (denyExtension) Name() string { return "deny-all" }
func (denyExtension) OnToolCall(_ context.Context, tcc *ToolCallContext) error {
	tcc.Decision = Deny
	tcc.Reason = "test policy"
	return nil
}

func TestRunTurnDenyDecision(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "dangerous", Input: `{}`}),
		{FinalAnswer: "denied result"},
	}}
	tools := newFakeTools(nil)
	fctx := newFakeContext()
	exts := NewExtensions(denyExtension{})

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) {
		cfg.Extensions = exts
	})
	tr, err := l.RunTurn(context.Background(), "do something dangerous")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "denied result" {
		t.Errorf("FinalAnswer = %q, want %q", tr.FinalAnswer, "denied result")
	}

	// Tool must NOT have been executed
	if len(tools.executed()) != 0 {
		t.Errorf("expected tool NOT to be executed, got %d executions", len(tools.executed()))
	}
}

// terminateExtension sets Terminate on the first iteration.
type terminateExtension struct {
	reason string
}

func (te terminateExtension) Name() string { return "terminator" }
func (te terminateExtension) BeforeIteration(_ context.Context, tc *TurnContext) error {
	tc.Terminate = te.reason
	return nil
}

func TestRunTurnHookTerminate(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		{FinalAnswer: "should not reach"},
	}}
	tools := newFakeTools(nil)
	fctx := newFakeContext()
	exts := NewExtensions(terminateExtension{reason: "budget exhausted"})

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) {
		cfg.Extensions = exts
	})
	tr, err := l.RunTurn(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Terminated != "budget exhausted" {
		t.Errorf("Terminated = %q, want %q", tr.Terminated, "budget exhausted")
	}
	// Model should NOT have been called
	if model.calls() != 0 {
		t.Errorf("expected 0 model calls, got %d", model.calls())
	}
}

func TestRunTurnCtxCancel(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		{FinalAnswer: "should not reach"},
	}}
	tools := newFakeTools(nil)
	fctx := newFakeContext()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	l := newTestLoop(t, model, tools, fctx)
	_, err := l.RunTurn(ctx, "hello")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %q, want substring %q", err.Error(), "canceled")
	}
}

func TestRunTurnInvalidFinalRetry(t *testing.T) {
	tests := []struct {
		name       string
		steps      []ReasoningResult
		wantAnswer string
		wantCalls  int
		wantErr    string // substring of the returned error; "" = success
		wantRetry  string // substring of the first retry message appended
	}{
		{
			name: "empty then valid",
			steps: []ReasoningResult{
				{FinalAnswer: ""},
				{FinalAnswer: "real answer"},
			},
			wantAnswer: "real answer",
			wantCalls:  2,
		},
		{
			name: "pseudo tool call then valid",
			steps: []ReasoningResult{
				{FinalAnswer: "<|tool_call>call:graph_cypher{...}"},
				{FinalAnswer: "real answer"},
			},
			wantAnswer: "real answer",
			wantCalls:  2,
		},
		{
			name: "empty, pseudo tool call, then valid",
			steps: []ReasoningResult{
				{FinalAnswer: ""},
				{FinalAnswer: "<|tool_call>call:graph_cypher{...}"},
				{FinalAnswer: "Total expenses were $1.2M."},
			},
			wantAnswer: "Total expenses were $1.2M.",
			wantCalls:  3,
		},
		{
			name: "truncated then valid",
			steps: []ReasoningResult{
				{FinalAnswer: "I can see there are two values. There could", Meta: ResponseMeta{StopReason: "max_tokens"}},
				{FinalAnswer: `{"metricName": "Total Assets", "value": {"2020": "$1,051,999"}}`, Meta: ResponseMeta{StopReason: "end_turn"}},
			},
			wantAnswer: `{"metricName": "Total Assets", "value": {"2020": "$1,051,999"}}`,
			wantCalls:  2,
		},
		{
			// Thinking ate the whole output cap: no text, stop_reason
			// max_tokens. The model must be told it was cut off, not that
			// it forgot to call a tool.
			name: "thinking-only truncation then valid",
			steps: []ReasoningResult{
				{FinalAnswer: "", Meta: ResponseMeta{StopReason: "max_tokens"}},
				{FinalAnswer: "real answer", Meta: ResponseMeta{StopReason: "end_turn"}},
			},
			wantAnswer: "real answer",
			wantCalls:  2,
			wantRetry:  "cut off by the output token limit",
		},
		{
			name: "gives up with pseudo tool call, returns it as-is",
			steps: []ReasoningResult{
				{FinalAnswer: "call:graph_cypher{a}"},
				{FinalAnswer: "call:graph_cypher{b}"},
				{FinalAnswer: "call:graph_cypher{c}"},
			},
			wantAnswer: "call:graph_cypher{c}",
			wantCalls:  1 + maxInvalidFinalRetries,
		},
		{
			name: "gives up empty, fails the turn",
			steps: []ReasoningResult{
				{FinalAnswer: "", Meta: ResponseMeta{StopReason: "max_tokens"}},
				{FinalAnswer: "", Meta: ResponseMeta{StopReason: "max_tokens"}},
				{FinalAnswer: "", Meta: ResponseMeta{StopReason: "max_tokens"}},
			},
			wantCalls: 1 + maxInvalidFinalRetries,
			wantErr:   "no output after 2 retries: the response was cut off",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &fakeModel{steps: tt.steps}
			tools := newFakeTools(nil)
			fctx := newFakeContext()

			l := newTestLoop(t, model, tools, fctx)
			tr, err := l.RunTurn(context.Background(), "test query")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tr.FinalAnswer != tt.wantAnswer {
				t.Errorf("FinalAnswer = %q, want %q", tr.FinalAnswer, tt.wantAnswer)
			}
			if model.calls() != tt.wantCalls {
				t.Errorf("model calls = %d, want %d", model.calls(), tt.wantCalls)
			}
			if tt.wantRetry != "" {
				// msgs: user query, assistant, retry user message, ...
				fctx.mu.Lock()
				retry := fctx.msgs[2]
				fctx.mu.Unlock()
				if got := retry.Content[0].Text; !strings.Contains(got, tt.wantRetry) {
					t.Errorf("retry message = %q, want substring %q", got, tt.wantRetry)
				}
			}
		})
	}
}

func TestNewLoopValidation(t *testing.T) {
	model := &fakeModel{}
	tools := newFakeTools(nil)
	fctx := newFakeContext()

	tests := []struct {
		name    string
		cfg     LoopConfig
		wantErr string
	}{
		{"missing ID", LoopConfig{Model: model, Tools: tools, Context: fctx}, "loop ID"},
		{"missing Model", LoopConfig{ID: "x", Tools: tools, Context: fctx}, "ModelPort"},
		{"missing Tools", LoopConfig{ID: "x", Model: model, Context: fctx}, "ToolPort"},
		{"missing Context", LoopConfig{ID: "x", Model: model, Tools: tools}, "ContextPort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLoop(tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestInvalidFinalAnswerReason(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		invalid bool
	}{
		{"plain prose", "Total expenses for 2021 were $1.2M.", false},
		{"empty", "", true},
		{"whitespace only", "   \n\t ", true},
		{"gemma corruption artifact", `<|tool_call>call:graph_aggregate{query:...}<tool_call|>`, true},
		{"bare call syntax", `call:graph_cypher{query: "MATCH (n) RETURN n"}`, true},
		{"call with paren", `call:llm_query("what is revenue")`, true},
		{"prose mentioning tools", "I used the graph_cypher tool to find the answer: $500.", false},
		{"json-ish but legitimate answer", `The values are {"jan": 100, "feb": 200}.`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := invalidFinalAnswerReason(tt.answer)
			if tt.invalid && reason == "" {
				t.Errorf("expected %q to be invalid", tt.answer)
			}
			if !tt.invalid && reason != "" {
				t.Errorf("expected %q to be valid, got reason %q", tt.answer, reason)
			}
		})
	}
}

// --- approval flow (Task 16) ----------------------------------------------

// askExtension escalates every tool call to AskUser.
type askExtension struct{}

func (askExtension) Name() string { return "ask-all" }
func (askExtension) OnToolCall(_ context.Context, tcc *ToolCallContext) error {
	if tcc.Decision < AskUser {
		tcc.Decision = AskUser
	}
	tcc.Reason = "needs approval"
	return nil
}

// respondingEmitter answers ApprovalRequestedEvents with a fixed verdict.
// respond == nil ignores the request (for timeout tests).
type respondingEmitter struct {
	fakeEmitter
	approve *bool // nil = never respond
}

func (e *respondingEmitter) Emit(ev Event) {
	e.fakeEmitter.Emit(ev)
	if req, ok := ev.(ApprovalRequestedEvent); ok && e.approve != nil {
		verdict := *e.approve
		go req.Respond(verdict)
	}
}

func newApprovalLoop(t *testing.T, emitter Emitter, timeout time.Duration) (*Loop, *fakeTools) {
	t.Helper()
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "bash", Input: `{"cmd":"ls"}`}),
		{FinalAnswer: "after approval"},
	}}
	tools := newFakeTools(map[string]ToolResult{"bash": {Output: "files"}})
	l := newTestLoop(t, model, tools, newFakeContext(), func(cfg *LoopConfig) {
		cfg.Emitter = emitter
		cfg.Extensions = NewExtensions(askExtension{})
		cfg.ApprovalTimeout = timeout
	})
	return l, tools
}

func TestRunTurnAskUserApproved(t *testing.T) {
	yes := true
	emitter := &respondingEmitter{approve: &yes}
	l, tools := newApprovalLoop(t, emitter, 5*time.Second)

	tr, err := l.RunTurn(context.Background(), "run it")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "after approval" {
		t.Errorf("FinalAnswer = %q", tr.FinalAnswer)
	}
	if got := len(tools.executed()); got != 1 {
		t.Fatalf("expected the approved tool to execute once, got %d", got)
	}
	found := false
	for _, et := range emitter.eventTypes() {
		if et == EventApprovalRequested {
			found = true
		}
	}
	if !found {
		t.Error("no ApprovalRequestedEvent emitted")
	}
}

func TestRunTurnAskUserDenied(t *testing.T) {
	no := false
	emitter := &respondingEmitter{approve: &no}
	l, tools := newApprovalLoop(t, emitter, 5*time.Second)

	tr, err := l.RunTurn(context.Background(), "run it")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "after approval" {
		t.Errorf("FinalAnswer = %q", tr.FinalAnswer)
	}
	if got := len(tools.executed()); got != 0 {
		t.Fatalf("denied tool must not execute, got %d executions", got)
	}
}

func TestRunTurnAskUserTimeout(t *testing.T) {
	emitter := &respondingEmitter{approve: nil} // never responds
	l, tools := newApprovalLoop(t, emitter, 10*time.Millisecond)

	tr, err := l.RunTurn(context.Background(), "run it")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FinalAnswer != "after approval" {
		t.Errorf("FinalAnswer = %q", tr.FinalAnswer)
	}
	if got := len(tools.executed()); got != 0 {
		t.Fatalf("timed-out tool must not execute, got %d executions", got)
	}
}

func TestRunTurnAskUserZeroTimeoutDeniesImmediately(t *testing.T) {
	emitter := &respondingEmitter{approve: nil}
	l, tools := newApprovalLoop(t, emitter, 0)

	if _, err := l.RunTurn(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if got := len(tools.executed()); got != 0 {
		t.Fatalf("zero-timeout AskUser must deny without executing, got %d", got)
	}
	for _, et := range emitter.eventTypes() {
		if et == EventApprovalRequested {
			t.Error("zero timeout must not emit an approval request")
		}
	}
}

func TestRunTurnSkipPermissionsAutoApproves(t *testing.T) {
	emitter := &respondingEmitter{approve: nil} // nobody to answer
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "bash", Input: `{"cmd":"ls"}`}),
		{FinalAnswer: "after approval"},
	}}
	tools := newFakeTools(map[string]ToolResult{"bash": {Output: "files"}})
	l := newTestLoop(t, model, tools, newFakeContext(), func(cfg *LoopConfig) {
		cfg.Emitter = emitter
		cfg.Extensions = NewExtensions(askExtension{})
		cfg.SkipPermissions = true
	})

	if _, err := l.RunTurn(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if got := len(tools.executed()); got != 1 {
		t.Fatalf("skip-permissions AskUser must auto-approve and execute, got %d", got)
	}
	for _, et := range emitter.eventTypes() {
		if et == EventApprovalRequested {
			t.Error("skip-permissions must not emit an approval request")
		}
	}
}

func TestRunTurnSkipPermissionsDoesNotOverrideDeny(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "bash", Input: `{"cmd":"ls"}`}),
		{FinalAnswer: "done"},
	}}
	tools := newFakeTools(map[string]ToolResult{"bash": {Output: "files"}})
	l := newTestLoop(t, model, tools, newFakeContext(), func(cfg *LoopConfig) {
		cfg.Extensions = NewExtensions(denyExtension{})
		cfg.SkipPermissions = true
	})

	if _, err := l.RunTurn(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if got := len(tools.executed()); got != 0 {
		t.Fatalf("Deny must still deny under skip-permissions, got %d executions", got)
	}
}

// usageCapturingExt records the TurnContext usage fields per iteration.
type usageCapturingExt struct {
	mu   sync.Mutex
	seen []TurnContext
}

func (u *usageCapturingExt) Name() string { return "usage-capture" }
func (u *usageCapturingExt) BeforeIteration(_ context.Context, tc *TurnContext) error {
	u.mu.Lock()
	u.seen = append(u.seen, *tc)
	u.mu.Unlock()
	return nil
}

func TestRunTurnPopulatesUsageInTurnContext(t *testing.T) {
	step1 := toolCallResult(ToolCall{ID: "tc1", Name: "search", Input: `{}`})
	step1.Meta.InputTokens = 100
	step1.Meta.OutputTokens = 40
	model := &fakeModel{steps: []ReasoningResult{
		step1,
		{FinalAnswer: "done", Meta: ResponseMeta{InputTokens: 200, OutputTokens: 60}},
	}}
	capture := &usageCapturingExt{}

	l := newTestLoop(t, model, newFakeTools(nil), newFakeContext(), func(cfg *LoopConfig) {
		cfg.Extensions = NewExtensions(capture)
	})
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(capture.seen) != 2 {
		t.Fatalf("iterations seen = %d, want 2", len(capture.seen))
	}
	first, second := capture.seen[0], capture.seen[1]
	if first.InputTokens != 0 || first.OutputTokens != 0 {
		t.Errorf("first iteration usage = %d/%d, want 0/0", first.InputTokens, first.OutputTokens)
	}
	if second.InputTokens != 100 || second.OutputTokens != 40 {
		t.Errorf("second iteration usage = %d/%d, want 100/40", second.InputTokens, second.OutputTokens)
	}
	if second.Elapsed <= 0 {
		t.Errorf("second iteration Elapsed = %v, want > 0", second.Elapsed)
	}
}

// --- batch concurrency (Task 19) ------------------------------------------

// sleepyTools sleeps per tool name and records completion times. All tools
// report read-only so batches exercise the concurrent path; set readOnly
// entries to false to make individual tools act as barriers.
type sleepyTools struct {
	mu        sync.Mutex
	delays    map[string]time.Duration
	completed map[string]time.Time
	readOnly  map[string]bool
}

func newSleepyTools(delays map[string]time.Duration) *sleepyTools {
	return &sleepyTools{delays: delays, completed: make(map[string]time.Time)}
}

func (s *sleepyTools) BeginTurn(_ context.Context)          {}
func (s *sleepyTools) Definitions() []common.ToolDefinition { return nil }
func (s *sleepyTools) Origin(string) string                 { return "native" }
func (s *sleepyTools) ReadOnly(name string) bool {
	if s.readOnly == nil {
		return true
	}
	ro, ok := s.readOnly[name]
	return !ok || ro
}
func (s *sleepyTools) Execute(_ context.Context, call ToolCall) ToolResult {
	time.Sleep(s.delays[call.Name])
	s.mu.Lock()
	s.completed[call.Name] = time.Now()
	s.mu.Unlock()
	return ToolResult{ToolUseID: call.ID, Output: call.Name}
}
func (s *sleepyTools) completedAt(name string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completed[name]
}

func TestBatchToolCallsRunConcurrently(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "1", Name: "a", Input: `{}`},
			ToolCall{ID: "2", Name: "b", Input: `{}`},
			ToolCall{ID: "3", Name: "c", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	tools := newSleepyTools(map[string]time.Duration{
		"a": 100 * time.Millisecond,
		"b": 100 * time.Millisecond,
		"c": 100 * time.Millisecond,
	})

	l := newTestLoop(t, model, tools, newFakeContext())
	start := time.Now()
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	wall := time.Since(start)
	// sum = 300ms; concurrent ≈ 100ms. Generous margin: must beat sum/2.
	if wall >= 150*time.Millisecond {
		t.Errorf("batch wall = %v, want < 150ms (concurrent execution)", wall)
	}
}

func TestBatchResultsKeepIssueOrder(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "slow", Name: "a", Input: `{}`},
			ToolCall{ID: "mid", Name: "b", Input: `{}`},
			ToolCall{ID: "fast", Name: "c", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	// completion order is reverse of issue order
	tools := newSleepyTools(map[string]time.Duration{
		"a": 90 * time.Millisecond,
		"b": 50 * time.Millisecond,
		"c": 5 * time.Millisecond,
	})
	fctx := newFakeContext()

	l := newTestLoop(t, model, tools, fctx)
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(fctx.toolResults) != 1 {
		t.Fatalf("AppendToolResults batches = %d, want 1", len(fctx.toolResults))
	}
	got := fctx.toolResults[0]
	wantIDs := []string{"slow", "mid", "fast"}
	if len(got) != len(wantIDs) {
		t.Fatalf("results = %d, want %d", len(got), len(wantIDs))
	}
	for i, id := range wantIDs {
		if got[i].ToolUseID != id {
			t.Errorf("results[%d].ToolUseID = %q, want %q (issue order)", i, got[i].ToolUseID, id)
		}
	}
}

// askOneExt escalates a single named tool to AskUser.
type askOneExt struct{ tool string }

func (e askOneExt) Name() string { return "ask-one" }
func (e askOneExt) OnToolCall(_ context.Context, tcc *ToolCallContext) error {
	if tcc.Call.Name == e.tool && tcc.Decision < AskUser {
		tcc.Decision = AskUser
	}
	return nil
}

// slowApprover approves after a delay, recording when it responded.
type slowApprover struct {
	fakeEmitter
	delay time.Duration

	mu          sync.Mutex
	respondedAt time.Time
}

func (e *slowApprover) Emit(ev Event) {
	e.fakeEmitter.Emit(ev)
	if req, ok := ev.(ApprovalRequestedEvent); ok {
		go func() {
			time.Sleep(e.delay)
			e.mu.Lock()
			e.respondedAt = time.Now()
			e.mu.Unlock()
			req.Respond(true)
		}()
	}
}

func (e *slowApprover) when() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.respondedAt
}

func TestBatchAskUserStragglerDoesNotBlockOthers(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "1", Name: "danger", Input: `{}`},
			ToolCall{ID: "2", Name: "fast1", Input: `{}`},
			ToolCall{ID: "3", Name: "fast2", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	tools := newSleepyTools(map[string]time.Duration{
		"danger": time.Millisecond,
		"fast1":  5 * time.Millisecond,
		"fast2":  5 * time.Millisecond,
	})
	approver := &slowApprover{delay: 150 * time.Millisecond}

	l := newTestLoop(t, model, tools, newFakeContext(), func(cfg *LoopConfig) {
		cfg.Emitter = approver
		cfg.Extensions = NewExtensions(askOneExt{tool: "danger"})
		cfg.ApprovalTimeout = 5 * time.Second
	})
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	respondTime := approver.when()
	if respondTime.IsZero() {
		t.Fatal("approver never responded")
	}
	for _, name := range []string{"fast1", "fast2"} {
		done := tools.completedAt(name)
		if done.IsZero() {
			t.Fatalf("%s never completed", name)
		}
		if !done.Before(respondTime) {
			t.Errorf("%s completed at %v, after approval at %v — others must not wait on the straggler", name, done, respondTime)
		}
	}
	if tools.completedAt("danger").IsZero() {
		t.Error("approved straggler never executed")
	}
}

func TestBatchEightConcurrentCallsContextSequence(t *testing.T) {
	calls := make([]ToolCall, 8)
	delays := make(map[string]time.Duration, 8)
	for i := range calls {
		name := fmt.Sprintf("t%d", i)
		calls[i] = ToolCall{ID: fmt.Sprintf("id%d", i), Name: name, Input: `{}`}
		delays[name] = time.Duration(i%4+1) * 5 * time.Millisecond
	}
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(calls...),
		{FinalAnswer: "done"},
	}}
	fctx := newFakeContext()

	l := newTestLoop(t, model, newSleepyTools(delays), fctx)
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	// The loop must only touch the context store after the barrier — the
	// call sequence is identical to the sequential implementation's.
	want := []string{"AppendUser", "Messages", "AppendAssistant", "AppendToolResults", "Messages", "AppendAssistant"}
	got := fctx.calls()
	if len(got) != len(want) {
		t.Fatalf("context calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("context call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// orderExt is both a batch and a per-call hook: it records the order the loop
// runs them in, denies one named tool from the batch phase, and refuses to
// execute anything the batch already denied.
type orderExt struct {
	mu      sync.Mutex
	order   []string
	denyRaw string
}

func (o *orderExt) Name() string { return "order" }

func (o *orderExt) OnToolBatch(_ context.Context, batch []*ToolCallContext) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	names := make([]string, len(batch))
	for i, tcc := range batch {
		names[i] = tcc.Call.Name
		if tcc.Call.Name == o.denyRaw {
			tcc.Decision = Deny
			tcc.Reason = "batch says no"
		}
	}
	o.order = append(o.order, "batch:"+strings.Join(names, ","))
	return nil
}

func (o *orderExt) OnToolCall(_ context.Context, tcc *ToolCallContext) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.order = append(o.order, "call:"+tcc.Call.Name+":"+decisionName(tcc.Decision))
	return nil
}

func decisionName(d Decision) string {
	switch d {
	case Deny:
		return "deny"
	case AskUser:
		return "ask"
	default:
		return "allow"
	}
}

// TestRunTurnToolBatchHookRunsBeforePerCallHooks pins the two guarantees the
// batch hook exists for: it sees the whole issue list once, before any
// per-call hook, and a decision it escalates is the decision the loop acts on.
func TestRunTurnToolBatchHookRunsBeforePerCallHooks(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "tc1", Name: "safe", Input: `{}`},
			ToolCall{ID: "tc2", Name: "dangerous", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	tools := newFakeTools(nil)
	ext := &orderExt{denyRaw: "dangerous"}

	l := newTestLoop(t, model, tools, newFakeContext(), func(cfg *LoopConfig) {
		cfg.Extensions = NewExtensions(ext)
	})
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	want := []string{"batch:safe,dangerous", "call:safe:allow", "call:dangerous:deny"}
	if len(ext.order) != len(want) {
		t.Fatalf("hook order = %v, want %v", ext.order, want)
	}
	for i := range want {
		if ext.order[i] != want[i] {
			t.Fatalf("hook order = %v, want %v", ext.order, want)
		}
	}

	executed := tools.executed()
	if len(executed) != 1 || executed[0].Name != "safe" {
		t.Fatalf("batch Deny must block execution, executed: %+v", executed)
	}
}

// failingBatchExt blocks every call of the batch with an error.
type failingBatchExt struct{ perCall int }

func (f *failingBatchExt) Name() string { return "failing-batch" }
func (f *failingBatchExt) OnToolBatch(_ context.Context, _ []*ToolCallContext) error {
	return errors.New("judge unreachable")
}
func (f *failingBatchExt) OnToolCall(_ context.Context, _ *ToolCallContext) error {
	f.perCall++
	return nil
}

// TestRunTurnToolBatchHookErrorBlocksTheBatch: a batch hook is load-bearing
// like OnToolCall, so its error blocks every call it judged — and the
// per-call hooks are skipped, since nothing will execute.
func TestRunTurnToolBatchHookErrorBlocksTheBatch(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "tc1", Name: "safe", Input: `{}`}),
		{FinalAnswer: "done"},
	}}
	tools := newFakeTools(nil)
	fctx := newFakeContext()
	ext := &failingBatchExt{}

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) {
		cfg.Extensions = NewExtensions(ext)
	})
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(tools.executed()) != 0 {
		t.Fatalf("batch hook error must block execution, executed %d", len(tools.executed()))
	}
	if ext.perCall != 0 {
		t.Fatalf("per-call hooks must be skipped when the batch is already blocked, ran %d", ext.perCall)
	}
	if len(fctx.toolResults) != 1 || !fctx.toolResults[0][0].IsError {
		t.Fatalf("blocked call must feed an error result back to the model: %+v", fctx.toolResults)
	}
}
