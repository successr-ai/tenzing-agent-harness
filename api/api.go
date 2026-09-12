// Package api exposes a harness over HTTP: an embedded index page, an SSE
// event stream, and typed JSON endpoints to drive agent turns. It is a
// transport adapter: requests map onto harness operations, harness events
// stream back out. The application builds the harness and injects it.
//
// Construction is two-phase because the harness streams text deltas through
// options that must exist before it does:
//
//	srv := api.New(api.ServerConfig{Bus: bus, ...})
//	h, _ := harness.New(llm, harness.WithTextDeltaHandler(srv.TextDelta), ...)
//	srv.Attach(h)
//	errCh, _ := srv.Start("127.0.0.1:8080")
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	httpserver "github.com/tab58/huma-http-server"
	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/api/costs"
	"github.com/successr-ai/tenzing-agent-harness/api/sse"
	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/api/ui"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Harness is the slice of *harness.Harness the routes drive.
type Harness interface {
	turnqueue.Runner
	// Steer injects msg into the running turn at the next tool boundary.
	Steer(msg string) error
	// LoopState reports the main runner's FSM state.
	LoopState() string
	// ConversationID is the main agent's conversation ID (resume handle).
	ConversationID() string
	// GetCurrentModel is the active model's wire name.
	GetCurrentModel() string
	// CurrentModel is the active model definition, or nil when unknown.
	CurrentModel() common.Model
	// SupportsVision reports whether the active model accepts image input.
	SupportsVision() bool
	// ToolDefinitions lists the registered tools.
	ToolDefinitions() []common.ToolDefinition
	// SessionInfo reports the session directory ("" when persistence is
	// disabled) and the working directory sessions are keyed by.
	SessionInfo() (dir, cwd string)
	// Clear starts a fresh conversation.
	Clear() error
	// Resume loads a recorded conversation.
	Resume(conversationID string) error
	// Compact summarizes the conversation so far.
	Compact(ctx context.Context, instructions string) error
	// SetThinking toggles model reasoning.
	SetThinking(enabled bool) error
	// SetLLM switches the main agent's model client.
	SetLLM(llm common.LLM) error
}

// EventBus is the harness's event bus. *eventbus.EventBus satisfies it.
type EventBus interface {
	// Subscribe returns a channel receiving every event, closed when the
	// bus closes.
	Subscribe(bufSize int) <-chan core.Event
	// Emit publishes an event to every subscriber.
	Emit(event core.Event)
}

// Nexus is the log-channel aggregator behind trigger-driven turns.
// *nexus.Nexus satisfies it.
type Nexus interface {
	// Read returns the last lastN entries of a channel, errors only when
	// errorsOnly is set.
	Read(name string, lastN int, errorsOnly bool) ([]nexus.Entry, error)
	// WebhookHandler ingests pushed log lines (POST /ingest/{name}).
	WebhookHandler() http.Handler
}

// LogStream serves the process's log output as SSE. *app.LogBroadcaster
// satisfies it.
type LogStream interface {
	// SSEHandler streams log lines to one client.
	SSEHandler() http.Handler
}

// AllowStore persists "allow always" bash globs. *app.BashAllowStore
// satisfies it.
type AllowStore interface {
	// Add persists pattern and applies it to the running session.
	Add(pattern string) error
	// Rules is the live rule set, for suggesting globs.
	Rules() *permissions.BashRules
}

// ServerConfig is everything the server needs besides the harness itself.
type ServerConfig struct {
	// Bus is the harness's event bus; the server forwards its events to SSE
	// clients. Required.
	Bus EventBus
	// Logs backs GET /debug. Optional: nil leaves the route unmounted.
	Logs LogStream
	// Nexus mounts POST /ingest/{name} and backs StartNexusTurn. Optional:
	// leave the field unset (not a typed nil) when there is no nexus.
	Nexus Nexus
	// OnTurnEnd is called each time the turn queue goes idle (the nexus
	// trigger flush hook). Optional.
	OnTurnEnd func()
	// Pricing maps lowercase model name to USD per MTok for cost tracking.
	// Optional: unpriced models report a null cost.
	Pricing map[string]config.CostEntry
	// ResolveLLM builds the client for a model ref, for POST /model.
	// Optional: nil rejects the endpoint with 400.
	ResolveLLM func(ref string) (common.LLM, error)
	// ModelNames lists the refs ResolveLLM accepts, for GET /models.
	// Optional: nil lists nothing.
	ModelNames func() []string
	// BashAllow persists "allow always" answers from POST /approve.
	// Optional: leave unset (not a typed nil) to reject the `allow` field.
	BashAllow AllowStore
	// Cwd is the working directory: reported by /state, used by /preview
	// and /trust.
	Cwd string
	// TrustEnvDefault is the TENZING_PROJECT_TRUST fallback for GET /trust:
	// "trust" grants, anything else skips.
	TrustEnvDefault string
	// Debug is substituted into the index page: only a debug UI renders
	// thinking text and tool output in full.
	Debug bool
}

// Server is the HTTP/SSE front end for one harness.
type Server struct {
	cfg     ServerConfig
	harness Harness // set by Attach
	http    *httpserver.Server[router.MapAuthInfo]

	turns     *turnqueue.Queue
	events    *sse.Broadcaster
	approvals *approvals.Registry
	costs     *costs.Tracker
}

// ErrNotAttached is returned by Start when no harness has been attached.
var ErrNotAttached = errors.New("api: no harness attached")

// New builds the server, mounts its routes, and starts forwarding bus
// events to SSE clients. The harness is attached separately (see Attach).
func New(cfg ServerConfig) *Server {
	s := &Server{
		cfg:       cfg,
		events:    sse.NewBroadcaster(),
		approvals: approvals.NewRegistry(),
		costs:     costs.NewTracker(cfg.Pricing),
	}
	s.turns = turnqueue.New(turnqueue.QueueConfig{
		Runner:    runnerFunc(s.runTurn),
		Events:    s.events,
		OnTurnEnd: cfg.OnTurnEnd,
	})
	s.http = httpserver.New(httpserver.ServerConfig{
		ServiceName:    "tenzing-agent",
		ServiceVersion: "0.1.0",
	}, router.MapAuthInfoBuilder)
	s.registerRoutes()

	if cfg.Bus != nil {
		go s.forwardEvents(cfg.Bus.Subscribe(256))
	}
	return s
}

// Attach binds the harness the routes drive. Call once, before Start.
func (s *Server) Attach(h Harness) { s.harness = h }

// TextDelta is a harness.WithTextDeltaHandler callback: it streams each
// text delta to SSE clients as a raw `text_delta` event.
func (s *Server) TextDelta(_, text string) { s.events.PublishRaw("text_delta", text) }

// ThinkingDelta is a harness.WithThinkingDeltaHandler callback for
// `thinking_delta` events.
func (s *Server) ThinkingDelta(_, text string) { s.events.PublishRaw("thinking_delta", text) }

// Start listens on addr. The returned channel reports the listener's exit.
func (s *Server) Start(addr string) (<-chan error, error) {
	if s.harness == nil {
		return nil, ErrNotAttached
	}
	return s.http.Start(addr)
}

// Shutdown cancels the in-flight turn, drops the queue, ends open SSE
// streams, and stops the HTTP listener. The harness and bus belong to the
// caller and are shut down separately, after this returns.
func (s *Server) Shutdown(ctx context.Context) error {
	s.turns.Close()
	s.events.Close()
	return s.http.Shutdown(ctx)
}

// runTurn is the queue's Runner: it forwards to whichever harness is
// attached at call time, so the queue can be built before Attach.
func (s *Server) runTurn(ctx context.Context, query string, images []common.ImageSource) (string, error) {
	return s.harness.RunTurnWithImages(ctx, query, images)
}

// runnerFunc adapts a function to turnqueue.Runner.
type runnerFunc func(ctx context.Context, query string, images []common.ImageSource) (string, error)

// RunTurnWithImages calls f. Conforms to the turnqueue.Runner interface.
func (f runnerFunc) RunTurnWithImages(ctx context.Context, query string, images []common.ImageSource) (string, error) {
	return f(ctx, query, images)
}

// registerRoutes mounts the API. The index page, SSE streams, and webhook
// are raw routes (they bypass huma middleware and the OpenAPI spec); the
// JSON endpoints are typed routes.
func (s *Server) registerRoutes() {
	srv := s.http
	srv.Handle("GET /", ui.Handler(s.cfg.Debug))
	srv.Handle("GET /events", s.events)

	route(srv, "query", http.MethodPost, "/query", s.handleQuery)
	route(srv, "cancel", http.MethodPost, "/cancel", s.handleCancel)
	route(srv, "preview", http.MethodPost, "/preview", s.handlePreview)
	route(srv, "suggest", http.MethodPost, "/suggest", s.handleSuggest)
	route(srv, "approve", http.MethodPost, "/approve", s.handleApprove)
	route(srv, "info", http.MethodGet, "/info", s.handleInfo)
	route(srv, "steer", http.MethodPost, "/steer", s.handleSteer)
	route(srv, "state", http.MethodGet, "/state", s.handleState)

	route(srv, "sessions-list", http.MethodGet, "/sessions", s.handleSessionsList)
	route(srv, "sessions-delete", http.MethodDelete, "/sessions/{id}", s.handleSessionDelete)
	route(srv, "sessions-rename", http.MethodPatch, "/sessions/{id}", s.handleSessionRename)
	route(srv, "messages", http.MethodGet, "/messages", s.handleMessages)
	route(srv, "clear", http.MethodPost, "/clear", s.handleClear)
	route(srv, "resume", http.MethodPost, "/resume", s.handleResume)
	route(srv, "compact", http.MethodPost, "/compact", s.handleCompact)
	route(srv, "thinking", http.MethodPost, "/thinking", s.handleThinking)
	route(srv, "model-set", http.MethodPost, "/model", s.handleModelSet)
	route(srv, "models-list", http.MethodGet, "/models", s.handleModelsList)
	route(srv, "stats", http.MethodGet, "/stats", s.handleStats)

	route(srv, "trust-get", http.MethodGet, "/trust", s.handleTrustGet)
	route(srv, "trust-set", http.MethodPost, "/trust", s.handleTrustSet)

	if s.cfg.Logs != nil {
		srv.Handle("GET /debug", s.cfg.Logs.SSEHandler())
	}
	if s.cfg.Nexus != nil {
		srv.Handle("POST /ingest/{name}", s.cfg.Nexus.WebhookHandler())
	}
}

// route registers one typed JSON operation.
func route[I, O any](srv *httpserver.Server[router.MapAuthInfo], id, method, path string,
	h func(context.Context, router.MapAuthInfo, *I) (*O, error)) {
	httpserver.RegisterRoute(srv, router.RegisterRouteArgs[I, O, router.MapAuthInfo]{
		Operation: huma.Operation{OperationID: id, Method: method, Path: path},
		Handler:   h,
	})
}
