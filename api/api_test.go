package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// --- test doubles ---

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
	return append([]string(nil), a.queries...)
}

// answerAgent completes every turn immediately with a fixed answer.
type answerAgent struct{}

func (a *answerAgent) GetCurrentModel() string               { return "ans" }
func (a *answerAgent) UpdateStreamCallback(_ func(string))   {}
func (a *answerAgent) UpdateThinkingCallback(_ func(string)) {}
func (a *answerAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	return core.ReasoningResult{
		FinalAnswer: "the-answer",
		Meta:        core.ResponseMeta{Model: "ans", AssistantText: "the-answer"},
	}, nil
}

// stubLLM satisfies common.LLM for harness construction; the agent builder
// ignores it, so no request ever reaches it.
type stubLLM struct{}

func (s *stubLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, _ chan<- common.StreamEvent) error {
	return nil
}
func (s *stubLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (s *stubLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (s *stubLLM) GetCurrentModel() string                                  { return "glm-5.3" }
func (s *stubLLM) GetContextWindowSize() int                                { return 131072 }
func (s *stubLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "glm-5.3", Provider: "local", ContextWindowSize: 131072}
}

// newTestServer builds a Server the way the container does — New, then a
// harness wired to its delta callbacks, then Attach — with the agent stubbed.
func newTestServer(t *testing.T, agent core.Agent, extraOpts ...harness.HarnessOption) *Server {
	t.Helper()
	bus := eventbus.NewEventBus()
	srv := New(ServerConfig{Bus: bus, Logs: app.NewLogBroadcaster()})
	h, err := harness.New(&stubLLM{},
		append([]harness.HarnessOption{
			harness.WithEventBus(bus),
			harness.WithTextDeltaHandler(srv.TextDelta),
			harness.WithThinkingDeltaHandler(srv.ThinkingDelta),
			harness.WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return agent, nil }),
			harness.WithSubagentDepth(0),
			// keep session files out of the real user config dir
			harness.WithSessionDir(t.TempDir()),
		}, extraOpts...)...,
	)
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	srv.Attach(h)
	t.Cleanup(func() {
		srv.turns.Close()
		srv.events.Close()
		h.Shutdown()
		bus.Close()
	})
	return srv
}

// submit starts or queues a plain query through the server's queue.
func submit(srv *Server, query string) turnqueue.Status {
	return srv.turns.Submit(turnqueue.Request{Query: query})
}

// idle reports whether no turn is running and nothing is queued.
func idle(srv *Server) func() bool {
	return func() bool {
		running, queued := srv.turns.State()
		return !running && queued == 0
	}
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

// safeRecorder is a goroutine-safe ResponseWriter+Flusher: the stream
// goroutine writes while the test reads.
type safeRecorder struct {
	mu   sync.Mutex
	hdr  http.Header
	code int
	buf  bytes.Buffer
}

func newSafeRecorder() *safeRecorder { return &safeRecorder{hdr: http.Header{}, code: http.StatusOK} }

func (r *safeRecorder) Header() http.Header { return r.hdr }
func (r *safeRecorder) WriteHeader(code int) {
	r.mu.Lock()
	r.code = code
	r.mu.Unlock()
}
func (r *safeRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}
func (r *safeRecorder) Flush() {}
func (r *safeRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// streamEvents connects an SSE client to srv and returns its recorder plus
// a function that ends the stream.
func streamEvents(t *testing.T, srv *Server) (*safeRecorder, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rec := newSafeRecorder()
	done := make(chan struct{})
	go func() {
		srv.events.ServeHTTP(rec, httptest.NewRequest("GET", "/events", nil).WithContext(ctx))
		close(done)
	}()
	// give the client a moment to subscribe: publish a probe until it shows
	waitFor(t, "sse client", func() bool {
		srv.events.PublishRaw("probe", "")
		return strings.Contains(rec.String(), "event: probe")
	})
	return rec, func() { cancel(); <-done }
}

// --- lifecycle ---

func TestNewWithoutBus(t *testing.T) {
	srv := New(ServerConfig{}) // no bus: nothing to forward, must not panic
	if srv.turns == nil || srv.events == nil || srv.approvals == nil || srv.costs == nil {
		t.Fatal("subpackage roots not built")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown of never-started server: %v", err)
	}
}

func TestStartRequiresAttach(t *testing.T) {
	srv := New(ServerConfig{})
	if _, err := srv.Start("127.0.0.1:0"); err != ErrNotAttached {
		t.Fatalf("Start before Attach: err = %v, want ErrNotAttached", err)
	}
}

func TestStartAndShutdown(t *testing.T) {
	srv := newTestServer(t, &answerAgent{})
	errCh, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("listener ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not report exit after Shutdown")
	}
	if submit(srv, "late") != turnqueue.Rejected {
		t.Error("queue accepted work after Shutdown")
	}
}

func TestDeltaCallbacksStreamRawText(t *testing.T) {
	srv := New(ServerConfig{})
	rec, end := streamEvents(t, srv)
	srv.TextDelta("main", "hel\nlo")
	srv.ThinkingDelta("main", "hmm")
	srv.TextDelta("main", "")
	waitFor(t, "deltas", func() bool { return strings.Contains(rec.String(), "event: thinking_delta") })
	end()
	body := rec.String()
	for _, want := range []string{
		"event: text_delta\ndata: hel\ndata: lo\n\n",
		"event: thinking_delta\ndata: hmm\n\n",
		"event: text_delta\ndata: \n\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q in %q", want, body)
		}
	}
}
