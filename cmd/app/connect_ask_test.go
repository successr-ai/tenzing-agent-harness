package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/wsclient"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// stepLLM answers each request with the next scripted response, and
// records the requests so a test can see what came back from a tool.
type stepLLM struct {
	scriptedLLM
	steps []common.CompletionResponse
	n     int
}

func (s *stepLLM) next(req common.CompletionRequest) common.CompletionResponse {
	s.record(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.steps[min(s.n, len(s.steps)-1)]
	s.n++
	return r
}

func (s *stepLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	return s.next(req), nil
}

func (s *stepLLM) SendMessageWithTools(_ context.Context, req common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return s.next(req), nil
}

func (s *stepLLM) SendStreamingMessage(_ context.Context, req common.CompletionRequest, events chan<- common.StreamEvent) error {
	resp := s.next(req)
	events <- common.StreamEvent{Type: common.StreamEventStop, Response: &resp}
	close(events)
	return nil
}

// toolThenAnswer scripts one tool call followed by a final answer.
func toolThenAnswer(tool string, input any, answer string) *stepLLM {
	raw, _ := json.Marshal(input)
	return &stepLLM{steps: []common.CompletionResponse{
		{
			Content:    []common.ContentBlock{common.NewToolUseContent("tu-1", tool, raw)},
			StopReason: common.StopReasonToolUse,
		},
		{Content: []common.ContentBlock{common.NewTextContent(answer)}, StopReason: common.StopReasonEndTurn},
	}}
}

// frameScript runs one query and answers the frames the turn blocks on
// with reply, until the result arrives. It records every frame it saw.
func frameScript(reply func(pc *e2eConn, m map[string]any), seen *[]map[string]any, mu *sync.Mutex) func(pc *e2eConn) {
	return func(pc *e2eConn) {
		pc.send(wsclient.Query{Type: "query", ID: "q1", Query: "go"})
		for {
			_, data, err := pc.conn.Read(pc.ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			mu.Lock()
			*seen = append(*seen, m)
			mu.Unlock()
			if m["type"] == "result" {
				break
			}
			reply(pc, m)
		}
		pc.send(wsclient.Shutdown{Type: "shutdown", ID: "s1"})
		pc.readUntil("shutdown_ack")
	}
}

func runAgainst(t *testing.T, plane *e2ePlane, llm *stepLLM, mutate func(*cliConfig)) {
	t.Helper()
	reg, err := buildTestRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &cliConfig{
		Model:          "alpha",
		NoContextFiles: true,
		Trust:          true,
		ConnectURL:     plane.url(),
		ConnectBackoff: 10 * time.Millisecond,
		deps:           &deps{models: reg, llms: &fakeLLMs{llm: llm}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := runConnect(ctx, cfg); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("runConnect only returned because the deadline cancelled it")
	}
}

func frameOf(seen []map[string]any, typ string) map[string]any {
	for _, m := range seen {
		if m["type"] == typ {
			return m
		}
	}
	return nil
}

// TestAskUserRoundTrip: ask_user sends the question to the plane as an
// input_request, blocks the turn, and returns the plane's answer to the
// model as the tool's result.
func TestAskUserRoundTrip(t *testing.T) {
	redirectUserConfig(t)
	t.Chdir(t.TempDir())
	var mu sync.Mutex
	var seen []map[string]any
	plane := newE2EPlane(t, frameScript(func(pc *e2eConn, m map[string]any) {
		if m["type"] == "input_request" {
			pc.send(wsclient.Answer{Type: "answer", ID: "c1", RequestID: m["id"].(string), Text: "only never-listed drafts"})
		}
	}, &seen, &mu))
	llm := toolThenAnswer("ask_user", map[string]string{"question": "Which drafts?"}, "understood")
	runAgainst(t, plane, llm, func(c *cliConfig) { c.SkipPermissions = true })

	mu.Lock()
	defer mu.Unlock()
	req := frameOf(seen, "input_request")
	if req == nil || req["question"] != "Which drafts?" || req["turn_id"] != "q1" {
		t.Fatalf("input_request = %v, want the question tagged with turn q1", req)
	}
	if res := frameOf(seen, "result"); res == nil || res["answer"] != "understood" {
		t.Errorf("result = %v, want the turn to finish after the answer", res)
	}
	reqs := llm.requests()
	if len(reqs) < 2 || !strings.Contains(toolOutputs(reqs[len(reqs)-1]), "only never-listed drafts") {
		t.Error("the model never saw the user's answer as the tool result")
	}
}

// TestEscalatedCallIsSentToThePlane: with an approval timeout configured,
// a call the policy escalates goes to the plane as an approval_request and
// waits for its decision. Before this nothing sent the frame, so every
// escalated call timed out as a denial the plane never saw.
func TestEscalatedCallIsSentToThePlane(t *testing.T) {
	redirectUserConfig(t)
	t.Chdir(t.TempDir())
	var mu sync.Mutex
	var seen []map[string]any
	plane := newE2EPlane(t, frameScript(func(pc *e2eConn, m map[string]any) {
		if m["type"] == "approval_request" {
			pc.send(wsclient.Approve{Type: "approve", ID: "a1", CallID: m["id"].(string), Approved: false})
		}
	}, &seen, &mu))
	llm := toolThenAnswer("bash", map[string]string{"command": "touch x"}, "done")
	runAgainst(t, plane, llm, func(c *cliConfig) {
		c.ApprovalTimeout, c.ApprovalTimeoutSet = time.Hour, true
	})

	mu.Lock()
	defer mu.Unlock()
	req := frameOf(seen, "approval_request")
	if req == nil || req["tool"] != "bash" || req["turn_id"] != "q1" {
		t.Fatalf("approval_request = %v, want the bash call tagged with turn q1", req)
	}
	if res := frameOf(seen, "result"); res == nil || res["outcome"] != "completed" {
		t.Errorf("result = %v, want the turn to finish once the plane decided", res)
	}
}

// toolOutputs flattens a request's tool results, where a tool's answer to
// the model lives (historyText reads only message text).
func toolOutputs(req common.CompletionRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, c := range m.Content {
			b.WriteString(c.ToolOutput)
		}
	}
	return b.String()
}

// TestAskUserIDsDoNotRepeatAcrossProcesses: each process builds its own
// tool, and the plane sees every attempt of a node; a counter alone
// re-issued ask-1 on every attempt.
func TestAskUserIDsDoNotRepeatAcrossProcesses(t *testing.T) {
	var ids []string
	record := func(id, _ string) { ids = append(ids, id) }
	for _, tool := range []*askUserTool{newAskUserTool(record), newAskUserTool(record)} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // returns as soon as the question is sent
		_, _ = tool.Execute(ctx, tooldef.ExecutionContext{Arguments: []string{`{"question":"q"}`}})
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Errorf("first ids of two processes = %v, want distinct", ids)
	}
}
