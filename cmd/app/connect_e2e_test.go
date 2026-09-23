package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/modelregistry"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wsclient"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// scriptedLLM answers every turn with one fixed text and records the
// requests it saw, so a resumed run can prove the prior history came back.
type scriptedLLM struct {
	answer string
	// thinking, when set, streams as reasoning ahead of the answer.
	thinking string
	mu       sync.Mutex
	reqs     []common.CompletionRequest
}

func (s *scriptedLLM) record(req common.CompletionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
}

func (s *scriptedLLM) requests() []common.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]common.CompletionRequest(nil), s.reqs...)
}

func (s *scriptedLLM) response() common.CompletionResponse {
	return common.CompletionResponse{
		Content:    []common.ContentBlock{common.NewTextContent(s.answer)},
		StopReason: common.StopReasonEndTurn,
	}
}

func (s *scriptedLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	s.record(req)
	return s.response(), nil
}

func (s *scriptedLLM) SendStreamingMessage(_ context.Context, req common.CompletionRequest, events chan<- common.StreamEvent) error {
	s.record(req)
	resp := s.response()
	if s.thinking != "" {
		events <- common.StreamEvent{Type: common.StreamEventThinking, Text: s.thinking}
	}
	events <- common.StreamEvent{Type: common.StreamEventDelta, Text: s.answer}
	events <- common.StreamEvent{Type: common.StreamEventStop, Response: &resp}
	close(events)
	return nil
}

func (s *scriptedLLM) SendMessageWithTools(_ context.Context, req common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	s.record(req)
	return s.response(), nil
}

func (s *scriptedLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (s *scriptedLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (s *scriptedLLM) GetCurrentModel() string                                  { return "glm-5.3" }
func (s *scriptedLLM) GetContextWindowSize() int                                { return 128000 }
func (s *scriptedLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "glm-5.3", ContextWindowSize: 128000, SupportsVision: true}
}

// fakeLLMs is the llmSource test double: every resolved model gets llm.
type fakeLLMs struct{ llm common.LLM }

func (f *fakeLLMs) Get(_ modelregistry.ResolvedModel) (common.LLM, error) { return f.llm, nil }

// e2ePlane is a minimal control-plane double for cmd/app: accepts the
// upgrade, records hello, answers welcome, then runs the script on that
// connection.
type e2ePlane struct {
	srv    *httptest.Server
	script func(pc *e2eConn)

	mu     sync.Mutex
	hellos []wsclient.Hello
}

type e2eConn struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func newE2EPlane(t *testing.T, script func(pc *e2eConn)) *e2ePlane {
	t.Helper()
	p := &e2ePlane{script: script}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{wsclient.Subprotocol}})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var h wsclient.Hello
		if json.Unmarshal(data, &h) != nil {
			return
		}
		p.mu.Lock()
		p.hellos = append(p.hellos, h)
		p.mu.Unlock()
		wb, _ := json.Marshal(wsclient.Welcome{Type: "welcome", AgentID: "agent-e2e"})
		if c.Write(ctx, websocket.MessageText, wb) != nil {
			return
		}
		p.script(&e2eConn{t: t, conn: c, ctx: ctx})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *e2ePlane) url() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

func (p *e2ePlane) helloList() []wsclient.Hello {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]wsclient.Hello(nil), p.hellos...)
}

func (pc *e2eConn) send(v any) {
	b, _ := json.Marshal(v)
	if err := pc.conn.Write(pc.ctx, websocket.MessageText, b); err != nil {
		pc.t.Logf("plane write: %v", err)
	}
}

// readUntil skips upstream messages (events, mostly) until one of the
// wanted type arrives; nil when the connection ends first.
func (pc *e2eConn) readUntil(typ string) map[string]any {
	for {
		_, data, err := pc.conn.Read(pc.ctx)
		if err != nil {
			pc.t.Logf("plane read while waiting for %s: %v", typ, err)
			return nil
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m["type"] == typ {
			return m
		}
	}
}

// oneTurnThenShutdown is the plane script shared by both runs: one query,
// wait for its result, then a graceful shutdown.
func oneTurnThenShutdown(query string, got *map[string]any) func(pc *e2eConn) {
	return func(pc *e2eConn) {
		pc.send(wsclient.Query{Type: "query", ID: "q-" + query, Query: query})
		*got = pc.readUntil("result")
		pc.send(wsclient.Shutdown{Type: "shutdown", ID: "s-" + query})
		if ack := pc.readUntil("shutdown_ack"); ack == nil || ack["id"] != "s-"+query {
			pc.t.Errorf("shutdown_ack = %v, want id s-%s", ack, query)
		}
	}
}

// historyText flattens a request's message texts for substring checks.
func historyText(req common.CompletionRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, c := range m.Content {
			b.WriteString(c.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestConnectResumeEndToEnd drives runConnect twice against a plane double:
// run 1 pre-assigns the conversation id, answers one query, and exits on a
// `shutdown` command; run 2 relaunches with --resume and must (a) report the
// same conversation_id in hello and (b) hand the model run 1's history.
func TestConnectResumeEndToEnd(t *testing.T) {
	redirectUserConfig(t)
	t.Chdir(t.TempDir())
	sessionDir := t.TempDir()
	reg, err := buildTestRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const convID = "conv-e2e"

	run := func(t *testing.T, llm *scriptedLLM, query string, mutate func(*cliConfig)) (map[string]any, []wsclient.Hello) {
		t.Helper()
		var result map[string]any
		plane := newE2EPlane(t, oneTurnThenShutdown(query, &result))
		cfg := &cliConfig{
			Model:                  "alpha",
			SkipPermissions:        true,
			NoContextFiles:         true,
			Trust:                  true,
			ConnectURL:             plane.url(),
			ConnectBackoff:         10 * time.Millisecond,
			ConnectEphemeralGrants: true,
			deps:                   &deps{models: reg, llms: &fakeLLMs{llm: llm}},
		}
		mutate(cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := runConnect(ctx, cfg, harness.WithSessionDir(sessionDir)); err != nil {
			t.Fatalf("runConnect: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatal("runConnect only returned because the test deadline cancelled it")
		}
		return result, plane.helloList()
	}

	// Run 1: fresh conversation under a pre-assigned id.
	llm1 := &scriptedLLM{answer: "answer one"}
	res1, hellos1 := run(t, llm1, "first question", func(c *cliConfig) { c.ConversationID = convID })
	if res1 == nil || res1["outcome"] != "completed" || res1["answer"] != "answer one" {
		t.Fatalf("run 1 result = %v, want completed/answer one", res1)
	}
	if len(hellos1) != 1 || hellos1[0].ConversationID != convID {
		t.Fatalf("run 1 hellos = %+v, want one hello with conversation_id %s", hellos1, convID)
	}

	// Run 2: relaunch with --resume; the plane sees the same id and the
	// model sees run 1's exchange ahead of the new question.
	llm2 := &scriptedLLM{answer: "answer two"}
	res2, hellos2 := run(t, llm2, "second question", func(c *cliConfig) { c.Resume = convID })
	if res2 == nil || res2["outcome"] != "completed" || res2["answer"] != "answer two" {
		t.Fatalf("run 2 result = %v, want completed/answer two", res2)
	}
	if len(hellos2) != 1 || hellos2[0].ConversationID != convID {
		t.Fatalf("run 2 hellos = %+v, want one hello with conversation_id %s", hellos2, convID)
	}
	reqs := llm2.requests()
	if len(reqs) == 0 {
		t.Fatal("run 2 model saw no requests")
	}
	hist := historyText(reqs[0])
	for _, want := range []string{"first question", "answer one", "second question"} {
		if !strings.Contains(hist, want) {
			t.Errorf("resumed history missing %q:\n%s", want, hist)
		}
	}
}

// TestConnectForwardsThinkingDeltas: connect mode streams the model's
// reasoning upstream as thinking_delta envelopes tagged with the running
// turn, so the plane can show it live.
func TestConnectForwardsThinkingDeltas(t *testing.T) {
	redirectUserConfig(t)
	t.Chdir(t.TempDir())
	reg, err := buildTestRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var thinking []string
	plane := newE2EPlane(t, func(pc *e2eConn) {
		pc.send(wsclient.Query{Type: "query", ID: "q-think", Query: "ponder"})
		for {
			_, data, err := pc.conn.Read(pc.ctx)
			if err != nil {
				return
			}
			var m struct {
				Type     string `json:"type"`
				ID       string `json:"id"`
				Envelope struct {
					Type string `json:"type"`
					Data struct {
						Text string `json:"text"`
					} `json:"data"`
				} `json:"envelope"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if m.Type == "result" {
				break
			}
			if m.Type == "event" && m.Envelope.Type == "thinking_delta" && m.ID == "q-think" {
				thinking = append(thinking, m.Envelope.Data.Text)
			}
		}
		pc.send(wsclient.Shutdown{Type: "shutdown", ID: "s-think"})
		pc.readUntil("shutdown_ack")
	})
	cfg := &cliConfig{
		Model:           "alpha",
		SkipPermissions: true,
		NoContextFiles:  true,
		Trust:           true,
		ConnectURL:      plane.url(),
		ConnectBackoff:  10 * time.Millisecond,
		deps:            &deps{models: reg, llms: &fakeLLMs{llm: &scriptedLLM{answer: "done", thinking: "hmm, let me see"}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := runConnect(ctx, cfg); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if got := strings.Join(thinking, ""); got != "hmm, let me see" {
		t.Fatalf("forwarded thinking = %q, want %q", got, "hmm, let me see")
	}
}
