package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	httpserver "github.com/tab58/huma-http-server"
	srverrors "github.com/tab58/huma-http-server/errors"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wire"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// agentServer exposes the harness over HTTP: an index page, an SSE event
// stream, and JSON endpoints to start/cancel agent turns.
type agentServer struct {
	harness   *harness.Harness
	bus       *eventbus.EventBus
	nexus     *nexus.Nexus // nil when no channels configured
	logB      *app.LogBroadcaster
	onTurnEnd func()         // trigger flush hook; called after every turn
	models    *modelRegistry // nil-safe: set by the container after construction
	costs     *costTracker

	// cwd / trustEnvDefault back the /trust endpoints; set by the container
	// after construction (like models).
	cwd             string
	trustEnvDefault string

	mu       sync.Mutex
	cancelFn context.CancelFunc
	closing  bool
	queue    []turnRequest // follow-up queries waiting for the current turn to end
	done     chan struct{} // closed on shutdown; unblocks SSE handlers

	clients   map[*sseClient]struct{}
	clientsMu sync.RWMutex

	// approvals holds pending AskUser responders keyed by tool-call ID.
	// Entries are removed when answered; timed-out entries are dropped when
	// the same call ID can no longer arrive (Respond is idempotent, so a
	// late answer to a timed-out request is a harmless no-op).
	approvals   map[string]func(bool)
	approvalsMu sync.Mutex
}

// newAgentServer builds the server. pricing (model name → USD per MTok) may
// be nil; it must be passed here rather than set later because the event
// forwarding goroutine starts reading the cost tracker immediately.
func newAgentServer(model common.ModelDefinition, bus *eventbus.EventBus, nx *nexus.Nexus, logB *app.LogBroadcaster, onTurnEnd func(), pricing map[string]costEntry, extraOpts ...harness.HarnessOption) (*agentServer, error) {
	s := &agentServer{
		bus:       bus,
		nexus:     nx,
		logB:      logB,
		onTurnEnd: onTurnEnd,
		done:      make(chan struct{}),
		clients:   make(map[*sseClient]struct{}),
		approvals: make(map[string]func(bool)),
		costs:     newCostTracker(pricing),
	}

	opts := append([]harness.HarnessOption{
		harness.WithEventBus(bus),
		harness.WithTextDeltaHandler(func(_, text string) { s.broadcastSSE("text_delta", text) }),
		harness.WithThinkingDeltaHandler(func(_, text string) { s.broadcastSSE("thinking_delta", text) }),
	}, extraOpts...)

	mainLLM, err := llms.get(model)
	if err != nil {
		return nil, err
	}
	h, err := harness.New(mainLLM, opts...)
	if err != nil {
		return nil, fmt.Errorf("harness init: %w", err)
	}
	s.harness = h

	evCh := bus.Subscribe(256)
	go s.forwardEvents(evCh)

	return s, nil
}

// registerRoutes mounts the API on the huma server. The index page and SSE
// stream are raw routes (they bypass huma middleware and the OpenAPI spec);
// the JSON endpoints are typed routes.
func (s *agentServer) registerRoutes(srv *httpserver.Server[router.MapAuthInfo]) {
	srv.Handle("GET /", http.HandlerFunc(s.handleIndex))
	srv.Handle("GET /events", http.HandlerFunc(s.handleSSE))

	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[queryInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "query",
			Method:      http.MethodPost,
			Path:        "/query",
		},
		Handler: s.handleQuery,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "cancel",
			Method:      http.MethodPost,
			Path:        "/cancel",
		},
		Handler: s.handleCancel,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[approveInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "approve",
			Method:      http.MethodPost,
			Path:        "/approve",
		},
		Handler: s.handleApprove,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, infoOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "info",
			Method:      http.MethodGet,
			Path:        "/info",
		},
		Handler: s.handleInfo,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[steerInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "steer",
			Method:      http.MethodPost,
			Path:        "/steer",
		},
		Handler: s.handleSteer,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, stateOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "state",
			Method:      http.MethodGet,
			Path:        "/state",
		},
		Handler: s.handleState,
	})

	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, sessionsOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "sessions-list",
			Method:      http.MethodGet,
			Path:        "/sessions",
		},
		Handler: s.handleSessionsList,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[sessionIDInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "sessions-delete",
			Method:      http.MethodDelete,
			Path:        "/sessions/{id}",
		},
		Handler: s.handleSessionDelete,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[sessionRenameInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "sessions-rename",
			Method:      http.MethodPatch,
			Path:        "/sessions/{id}",
		},
		Handler: s.handleSessionRename,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, messagesOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "messages",
			Method:      http.MethodGet,
			Path:        "/messages",
		},
		Handler: s.handleMessages,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[compactInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "compact",
			Method:      http.MethodPost,
			Path:        "/compact",
		},
		Handler: s.handleCompact,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[thinkingInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "thinking",
			Method:      http.MethodPost,
			Path:        "/thinking",
		},
		Handler: s.handleThinking,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[modelInput, statusOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "model-set",
			Method:      http.MethodPost,
			Path:        "/model",
		},
		Handler: s.handleModelSet,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, modelsOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "models-list",
			Method:      http.MethodGet,
			Path:        "/models",
		},
		Handler: s.handleModelsList,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, statsOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "stats",
			Method:      http.MethodGet,
			Path:        "/stats",
		},
		Handler: s.handleStats,
	})

	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[struct{}, trustOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "trust-get",
			Method:      http.MethodGet,
			Path:        "/trust",
		},
		Handler: s.handleTrustGet,
	})
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[trustInput, trustOutput, router.MapAuthInfo]{
		Operation: huma.Operation{
			OperationID: "trust-set",
			Method:      http.MethodPost,
			Path:        "/trust",
		},
		Handler: s.handleTrustSet,
	})

	srv.Handle("GET /debug", s.logB.SSEHandler())
	if s.nexus != nil {
		srv.Handle("POST /ingest/{name}", s.nexus.WebhookHandler())
	}
}

// --- SSE broadcast ---

type sseClient struct {
	ch chan sseMessage
}

type sseMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

func (s *agentServer) addSSEClient(c *sseClient) {
	s.clientsMu.Lock()
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()
}

func (s *agentServer) removeSSEClient(c *sseClient) {
	s.clientsMu.Lock()
	delete(s.clients, c)
	s.clientsMu.Unlock()
}

func (s *agentServer) broadcastSSE(event, data string) {
	msg := sseMessage{Event: event, Data: data}
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for c := range s.clients {
		select {
		case c.ch <- msg:
		default:
		}
	}
}

func (s *agentServer) broadcastSSEJSON(event string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("sse json marshal", "error", err)
		return
	}
	s.broadcastSSE(event, string(b))
}

// sseEnvelope is one SSE payload: the wire envelope plus the server-side
// agent label (blackboard slot name) for events from subagent runners. The
// SSE event name is the envelope's Type.
type sseEnvelope struct {
	wire.Envelope
	Agent string `json:"agent,omitempty"`
}

// translateSSE maps one bus event to its SSE message via the wire schema.
// subagents maps runner IDs to blackboard slot names ("a1", ...); ok=false
// means the event type is not forwarded to the UI.
func translateSSE(ev core.Event, subagents map[string]string) (event string, payload sseEnvelope, ok bool) {
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
		core.ImagesAttachedEvent,
		nexus.ChannelErrorEvent, nexus.ChannelStatusEvent, nexus.TriggerEvent:
		// forwarded without an agent label
	default:
		return "", sseEnvelope{}, false
	}
	env := wire.ToWire(ev)
	return env.Type, sseEnvelope{Envelope: env, Agent: agent}, true
}

func (s *agentServer) forwardEvents(ch <-chan core.Event) {
	// Sub-agent runners share the bus; map their runner IDs to blackboard
	// slot names ("a1", ...) so tool events can be labeled per agent. Only
	// this goroutine touches the map.
	subagents := make(map[string]string)
	for ev := range ch {
		// Side effects the pure translation can't own: subagent-label
		// bookkeeping, approval-responder capture, and cost accounting.
		switch e := ev.(type) {
		case core.SubagentStartedEvent:
			subagents[e.RunnerID] = e.AgentID
		case core.SubagentStoppedEvent:
			delete(subagents, e.RunnerID)
		case core.ApprovalRequestedEvent:
			s.approvalsMu.Lock()
			s.approvals[e.CallID] = e.Respond
			s.approvalsMu.Unlock()
		case core.LLMResponseEvent:
			s.costs.track(e)
		}
		if name, payload, ok := translateSSE(ev, subagents); ok {
			s.broadcastSSEJSON(name, payload)
		}
		if _, ok := ev.(core.LLMResponseEvent); ok {
			// Running totals ride behind every llm.response so the UI can
			// show live cost without polling /stats.
			s.broadcastSSEJSON("cost", s.costs.stats())
		}
	}
}

// --- Handlers ---

func (s *agentServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	client := &sseClient{
		ch: make(chan sseMessage, 128),
	}
	s.addSSEClient(client)
	defer s.removeSSEClient(client)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			// server shutdown: end the stream so http.Server.Shutdown can
			// drain — it waits for handlers but never cancels r.Context().
			return
		case msg := <-client.ch:
			data := strings.ReplaceAll(msg.Data, "\n", "\ndata: ")
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Event, data)
			flusher.Flush()
		}
	}
}

type queryInput struct {
	Body struct {
		Query  string       `json:"query" doc:"Prompt to run the agent with"`
		Images []imageInput `json:"images,omitempty" doc:"Images attached to the query (vision-capable models only)"`
	}
}

type imageInput struct {
	MediaType string `json:"media_type" doc:"Image MIME type, e.g. image/png"`
	Data      string `json:"data" doc:"Base64-encoded image bytes (no data: URI prefix)"`
}

type statusOutput struct {
	Body struct {
		Status string `json:"status" doc:"Result of the request"`
	}
}

type infoOutput struct {
	Body struct {
		Tools int `json:"tools" doc:"Number of registered tools"`
	}
}

type steerInput struct {
	Body struct {
		Message string `json:"message" doc:"User message to inject into the running turn at the next tool boundary"`
	}
}

type stateOutput struct {
	Body struct {
		State          string `json:"state" doc:"running or idle"`
		LoopState      string `json:"loop_state" doc:"Main runner FSM state"`
		Queued         int    `json:"queued" doc:"Follow-up queries waiting for the current turn to end"`
		ConversationID string `json:"conversation_id" doc:"Main agent conversation ID (resume handle)"`
		Model          string `json:"model" doc:"Active model"`
		Vision         bool   `json:"vision" doc:"True when the active model accepts image input"`
		Tools          int    `json:"tools" doc:"Number of registered tools"`
	}
}

type sessionsOutput struct {
	Body struct {
		Sessions []sessionInfo `json:"sessions" doc:"Sessions recorded for this working directory, newest first"`
	}
}

type sessionInfo struct {
	ConversationID string `json:"conversation_id"`
	Name           string `json:"name,omitempty" doc:"User-assigned name, empty when never renamed"`
	Model          string `json:"model"`
	Created        string `json:"created"`
	Modified       string `json:"modified"`
	Entries        int    `json:"entries"`
	Active         bool   `json:"active" doc:"True for the currently running conversation"`
}

type sessionIDInput struct {
	ID string `path:"id" doc:"Conversation ID"`
}

type sessionRenameInput struct {
	ID   string `path:"id" doc:"Conversation ID"`
	Body struct {
		Name string `json:"name" doc:"New session name"`
	}
}

type messagesOutput struct {
	Body struct {
		ConversationID string           `json:"conversation_id"`
		Messages       []messageSummary `json:"messages" doc:"Conversation history reconstructed from the session log"`
	}
}

type messageSummary struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type compactInput struct {
	Body struct {
		Instructions string `json:"instructions,omitempty" doc:"Optional steering for the summary"`
	}
}

type thinkingInput struct {
	Body struct {
		Enabled bool `json:"enabled" doc:"Turn model reasoning on or off"`
	}
}

type trustInput struct {
	Body struct {
		Trusted bool `json:"trusted" doc:"Whether project-local config in the server's working directory may be loaded"`
	}
}

type trustOutput struct {
	Body struct {
		Cwd     string `json:"cwd" doc:"Directory the decision applies to"`
		Trusted bool   `json:"trusted" doc:"Current trust decision"`
		Source  string `json:"source" doc:"Where the decision came from: persisted, env, default, or error"`
		Note    string `json:"note,omitempty" doc:"Additional context"`
	}
}

type modelInput struct {
	Body struct {
		Model string `json:"model" doc:"Model ref as provider/model-name"`
	}
}

type modelsOutput struct {
	Body struct {
		Current string   `json:"current" doc:"Active model name"`
		Models  []string `json:"models" doc:"Resolvable provider/model-name refs"`
	}
}

type statsOutput struct {
	Body costStats
}

// turnRequest is one agent turn's input: the query plus optional images.
type turnRequest struct {
	query  string
	images []common.ImageSource
}

// startTurn begins an agent turn for query. Returns false when a turn is
// already running. Used by nexus wakes, which must NOT queue: the trigger
// keeps its channels pending and retries after the turn ends.
func (s *agentServer) startTurn(query string) bool {
	s.mu.Lock()
	if s.closing || s.cancelFn != nil {
		s.mu.Unlock()
		return false
	}
	// the turn outlives the HTTP request, so it gets its own context
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel
	s.mu.Unlock()

	s.runTurn(ctx, cancel, turnRequest{query: query})
	return true
}

// startTurnOrQueue starts a turn immediately, or queues the request as a
// follow-up when one is already running. Returns "started", "queued", or
// "rejected" (shutting down). Used by the HTTP /query handler.
func (s *agentServer) startTurnOrQueue(req turnRequest) string {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return "rejected"
	}
	if s.cancelFn != nil {
		s.queue = append(s.queue, req)
		pos := len(s.queue)
		s.mu.Unlock()
		s.broadcastSSEJSON("queued", map[string]any{"query": req.query, "position": pos})
		return "queued"
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel
	s.mu.Unlock()

	s.runTurn(ctx, cancel, req)
	return "started"
}

// runTurn runs one agent turn in a goroutine. The busy slot (cancelFn) must
// already be claimed by the caller; finishTurn releases it or chains into
// the next queued query.
func (s *agentServer) runTurn(ctx context.Context, cancel context.CancelFunc, req turnRequest) {
	s.broadcastSSEJSON("status", map[string]string{"state": "running", "query": req.query})

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("agent panic", "error", rec)
				s.broadcastSSEJSON("error", map[string]string{"error": fmt.Sprintf("panic: %v", rec)})
			}
			cancel()
			s.finishTurn()
		}()

		answer, err := s.harness.RunTurnWithImages(ctx, req.query, req.images)
		if err != nil {
			s.broadcastSSEJSON("error", map[string]string{"error": err.Error()})
			return
		}
		s.broadcastSSEJSON("answer", map[string]string{"text": answer})
	}()
}

// validateImages checks media types and base64 payloads at the trust
// boundary, returning provider-ready image sources.
func validateImages(in []imageInput) ([]common.ImageSource, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]common.ImageSource, len(in))
	for i, img := range in {
		if !strings.HasPrefix(img.MediaType, "image/") {
			return nil, fmt.Errorf("images[%d]: media_type %q is not an image MIME type", i, img.MediaType)
		}
		if img.Data == "" {
			return nil, fmt.Errorf("images[%d]: empty data", i)
		}
		if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
			return nil, fmt.Errorf("images[%d]: data is not valid base64: %v", i, err)
		}
		out[i] = common.ImageSource{MediaType: img.MediaType, Data: img.Data}
	}
	return out, nil
}

// finishTurn releases the busy slot. When follow-ups are queued it keeps the
// slot claimed and chains directly into the next turn — new submissions keep
// queueing behind it, and nexus wakes stay deferred until the queue drains.
func (s *agentServer) finishTurn() {
	s.mu.Lock()
	if !s.closing && len(s.queue) > 0 {
		next := s.queue[0]
		s.queue = s.queue[1:]
		ctx, cancel := context.WithCancel(context.Background())
		s.cancelFn = cancel
		s.mu.Unlock()
		s.runTurn(ctx, cancel, next)
		return
	}
	s.cancelFn = nil
	s.mu.Unlock()
	s.broadcastSSEJSON("status", map[string]string{"state": "idle"})
	if s.onTurnEnd != nil {
		s.onTurnEnd()
	}
}

func (s *agentServer) handleQuery(_ context.Context, _ router.MapAuthInfo, in *queryInput) (*statusOutput, error) {
	query := strings.TrimSpace(in.Body.Query)
	if query == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty query")
	}
	images, err := validateImages(in.Body.Images)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, err.Error())
	}
	// Capability pre-flight: reject before the turn starts (the turn itself
	// runs async, so its errors can only surface on the SSE stream).
	if len(images) > 0 && !s.harness.SupportsVision() {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest,
			fmt.Sprintf("model %q does not support image input", s.harness.GetCurrentModel()))
	}
	status := s.startTurnOrQueue(turnRequest{query: query, images: images})
	if status == "rejected" {
		return nil, srverrors.Wrap(srverrors.ErrConflict, "server shutting down")
	}
	out := &statusOutput{}
	out.Body.Status = status
	return out, nil
}

func (s *agentServer) handleSteer(_ context.Context, _ router.MapAuthInfo, in *steerInput) (*statusOutput, error) {
	msg := strings.TrimSpace(in.Body.Message)
	if msg == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty message")
	}
	s.mu.Lock()
	running := s.cancelFn != nil
	s.mu.Unlock()
	if !running {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "nothing running — use /query")
	}
	if err := s.harness.Steer(msg); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "steering"
	return out, nil
}

func (s *agentServer) handleState(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*stateOutput, error) {
	s.mu.Lock()
	running := s.cancelFn != nil
	queued := len(s.queue)
	s.mu.Unlock()

	out := &stateOutput{}
	out.Body.State = "idle"
	if running {
		out.Body.State = "running"
	}
	out.Body.LoopState = s.harness.LoopState()
	out.Body.Queued = queued
	out.Body.ConversationID = s.harness.ConversationID()
	out.Body.Model = s.harness.GetCurrentModel()
	out.Body.Vision = s.harness.SupportsVision()
	out.Body.Tools = len(s.harness.ToolDefinitions())
	return out, nil
}

// startNexusTurn is the trigger wake callback: builds an investigation
// prompt from recent channel errors and starts a turn. Returns false when
// the agent is busy (trigger keeps the channels pending).
func (s *agentServer) startNexusTurn(channels []string) bool {
	if s.nexus == nil {
		return false
	}
	prompt := s.nexusPrompt(channels)
	if !s.startTurn(prompt) {
		return false
	}
	s.bus.Emit(nexus.TriggerEvent{
		BaseEvent: core.NewBaseEvent(nexus.EventTrigger, "nexus"),
		Channels:  channels,
	})
	return true
}

func (s *agentServer) nexusPrompt(channels []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Error detected in channel(s) %s.\n", strings.Join(channels, ", "))
	for _, name := range channels {
		entries, err := s.nexus.Read(name, 5, true)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\nRecent errors from %q:\n", name)
		for _, e := range entries {
			fmt.Fprintf(&b, "  [%d] %s\n", e.Seq, e.Text)
		}
	}
	b.WriteString("\nUse the read_channel and search_channel tools for more context. Investigate the root cause and report what you find.")
	return b.String()
}

// cancelActiveTurn cancels the in-flight agent turn, if any, drops the
// queue, and closes done so open SSE streams end. Used by container
// shutdown so a running turn doesn't outlive the harness. Idempotent.
func (s *agentServer) cancelActiveTurn() {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		close(s.done)
	}
	s.queue = nil
	if s.cancelFn != nil {
		s.cancelFn()
	}
	s.mu.Unlock()
}

// handleCancel cancels the in-flight turn AND drops any queued follow-ups:
// a user cancelling wants the agent to stop, not to watch the queue start
// the next turn.
func (s *agentServer) handleCancel(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*statusOutput, error) {
	s.mu.Lock()
	cancel := s.cancelFn
	dropped := len(s.queue)
	s.queue = nil
	s.mu.Unlock()

	if cancel == nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "nothing running")
	}
	cancel()
	out := &statusOutput{}
	out.Body.Status = "cancelled"
	if dropped > 0 {
		out.Body.Status = fmt.Sprintf("cancelled (%d queued queries dropped)", dropped)
	}
	return out, nil
}

type approveInput struct {
	Body struct {
		CallID   string `json:"call_id" doc:"Tool-call ID from the approval_requested event"`
		Approved bool   `json:"approved" doc:"true to run the tool, false to deny"`
	}
}

func (s *agentServer) handleApprove(_ context.Context, _ router.MapAuthInfo, in *approveInput) (*statusOutput, error) {
	s.approvalsMu.Lock()
	respond, ok := s.approvals[in.Body.CallID]
	delete(s.approvals, in.Body.CallID)
	s.approvalsMu.Unlock()

	if !ok {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "no pending approval for call_id")
	}
	respond(in.Body.Approved)

	out := &statusOutput{}
	if in.Body.Approved {
		out.Body.Status = "approved"
	} else {
		out.Body.Status = "denied"
	}
	return out, nil
}

func (s *agentServer) handleInfo(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*infoOutput, error) {
	out := &infoOutput{}
	out.Body.Tools = len(s.harness.ToolDefinitions())
	return out, nil
}

// --- Project trust (F26) ---

// handleTrustGet reports the trust decision for the server's cwd.
func (s *agentServer) handleTrustGet(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*trustOutput, error) {
	path, err := trustFilePath()
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	trusted, source := resolveProjectTrust(path, s.cwd, s.trustEnvDefault)
	out := &trustOutput{}
	out.Body.Cwd = s.cwd
	out.Body.Trusted = trusted
	out.Body.Source = source
	return out, nil
}

// handleTrustSet persists a trust decision for the server's cwd. Project
// config files are read at startup, so the decision applies on restart.
func (s *agentServer) handleTrustSet(_ context.Context, _ router.MapAuthInfo, in *trustInput) (*trustOutput, error) {
	path, err := trustFilePath()
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	if err := setProjectTrust(path, s.cwd, in.Body.Trusted, time.Now()); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	out := &trustOutput{}
	out.Body.Cwd = s.cwd
	out.Body.Trusted = in.Body.Trusted
	out.Body.Source = "persisted"
	out.Body.Note = "applies to project config loaded at startup; restart to take effect"
	return out, nil
}

// --- Runtime controls: compaction, thinking, model, stats ---

func (s *agentServer) handleCompact(ctx context.Context, _ router.MapAuthInfo, in *compactInput) (*statusOutput, error) {
	if err := s.harness.Compact(ctx, strings.TrimSpace(in.Body.Instructions)); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "compacted"
	return out, nil
}

func (s *agentServer) handleThinking(_ context.Context, _ router.MapAuthInfo, in *thinkingInput) (*statusOutput, error) {
	if err := s.harness.SetThinking(in.Body.Enabled); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "thinking off"
	if in.Body.Enabled {
		out.Body.Status = "thinking on"
	}
	return out, nil
}

func (s *agentServer) handleModelSet(_ context.Context, _ router.MapAuthInfo, in *modelInput) (*statusOutput, error) {
	if s.models == nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "no model registry configured")
	}
	def, err := s.models.resolve(strings.TrimSpace(in.Body.Model))
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, err.Error())
	}
	llm, err := llms.get(def)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, err.Error())
	}
	if err := s.harness.SetLLM(llm); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrConflict, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "model set to " + def.Name
	return out, nil
}

func (s *agentServer) handleModelsList(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*modelsOutput, error) {
	out := &modelsOutput{}
	out.Body.Current = s.harness.GetCurrentModel()
	if s.models != nil {
		out.Body.Models = s.models.availableRefs()
	}
	return out, nil
}

func (s *agentServer) handleStats(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*statsOutput, error) {
	return &statsOutput{Body: s.costs.stats()}, nil
}

// --- Session management (F4) ---

// sessionDir returns the harness's session directory, or an error when
// persistence is disabled.
func (s *agentServer) sessionDir() (dir, cwd string, err error) {
	dir, cwd = s.harness.SessionInfo()
	if dir == "" {
		return "", "", srverrors.Wrap(srverrors.ErrBadRequest, "session persistence is disabled")
	}
	return dir, cwd, nil
}

func (s *agentServer) handleSessionsList(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*sessionsOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	infos, err := session.List(dir, cwd)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	active := s.harness.ConversationID()
	out := &sessionsOutput{}
	out.Body.Sessions = make([]sessionInfo, len(infos))
	for i, in := range infos {
		out.Body.Sessions[i] = sessionInfo{
			ConversationID: in.ConversationID,
			Name:           in.Name,
			Model:          in.Model,
			Created:        in.Created.Format(time.RFC3339),
			Modified:       in.Modified.Format(time.RFC3339),
			Entries:        in.Entries,
			Active:         in.ConversationID == active,
		}
	}
	return out, nil
}

func (s *agentServer) handleSessionDelete(_ context.Context, _ router.MapAuthInfo, in *sessionIDInput) (*statusOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	// The live store holds an open handle on the active session; deleting
	// it would orphan every subsequent write.
	if in.ID == s.harness.ConversationID() {
		return nil, srverrors.Wrap(srverrors.ErrConflict, "cannot delete the active conversation")
	}
	if err := session.Delete(dir, cwd, in.ID); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrNotFound, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "deleted"
	return out, nil
}

func (s *agentServer) handleSessionRename(_ context.Context, _ router.MapAuthInfo, in *sessionRenameInput) (*statusOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, srverrors.Wrap(srverrors.ErrBadRequest, "empty name")
	}
	if err := session.Rename(dir, cwd, in.ID, name); err != nil {
		return nil, srverrors.Wrap(srverrors.ErrNotFound, err.Error())
	}
	out := &statusOutput{}
	out.Body.Status = "renamed"
	return out, nil
}

func (s *agentServer) handleMessages(_ context.Context, _ router.MapAuthInfo, _ *struct{}) (*messagesOutput, error) {
	dir, cwd, err := s.sessionDir()
	if err != nil {
		return nil, err
	}
	id := s.harness.ConversationID()
	res, err := session.Load(dir, cwd, id)
	if err != nil {
		return nil, srverrors.Wrap(srverrors.ErrInternalServerError, err.Error())
	}
	out := &messagesOutput{}
	out.Body.ConversationID = id
	out.Body.Messages = []messageSummary{}
	if res == nil {
		return out, nil
	}
	for _, m := range res.History {
		var text strings.Builder
		for _, b := range m.Content {
			text.WriteString(b.Text)
		}
		out.Body.Messages = append(out.Body.Messages, messageSummary{
			Role: string(m.Role),
			Text: text.String(),
		})
	}
	return out, nil
}

func (s *agentServer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}
