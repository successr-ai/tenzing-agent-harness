package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	app "github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wire"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wsclient"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// runConnect runs the agent in control-plane mode (--connect): dial the
// plane, register, execute turns from plane commands, stream events back.
// No HTTP listener, no embedded UI, no nexus. Turn execution reuses the
// harness's own cancellation path — each turn's context derives from the
// connection context inside wsclient, so a dropped socket cancels the
// in-flight turn (the loop's clean "turn canceled" outcome) and the wiring
// stops forwarding that turn's events. Every event a turn emits is forwarded
// before its result: the turn flushes the forwarder before it reports.
func runConnect(ctx context.Context, cfg *cliConfig, extraOpts ...harness.HarnessOption) error {
	model, err := cfg.deps.resolve(cfg.Model)
	if err != nil {
		return err
	}
	logFile, err := setupLogging(cfg.Debug, nil)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Trust + project config, identical to serve mode.
	trusted := cfg.Trust
	if !trusted {
		if trustPath, err := app.TrustFilePath(); err == nil {
			trusted, _ = app.ResolveProjectTrust(trustPath, cwd, cfg.ProjectTrust)
		}
	}
	cfgDir, _ := os.UserConfigDir()
	pc := loadProjectConfig(cwd, cfgDir, trusted)
	pc.logDecisions()

	opts := pc.harnessOpts()
	cliOpts, err := harnessOptions(cfg)
	if err != nil {
		return err
	}
	opts = append(opts, cliOpts...)
	// Connect mode is unattended: deny mutating tools instantly unless the
	// user configured otherwise (same headless default as print mode).
	if !cfg.ApprovalTimeoutSet && !cfg.NoPermissions && !cfg.SkipPermissions {
		opts = append(opts, harness.WithApprovalTimeout(0))
	}
	// Thinking deltas never reach the bus (serve and print register the
	// same handler), so they go upstream directly, tagged like bus events.
	// client is assigned below; deltas only flow during a turn, after Run.
	var client *wsclient.Client
	// ask_user reaches the plane's user; its answer arrives via Answer.
	ask := newAskUserTool(func(id, question string) {
		if client != nil {
			client.RequestInput(client.Running(), id, question)
		}
	})
	opts = append(opts, harness.WithTool(ask))
	opts = append(opts, harness.WithThinkingDeltaHandler(func(runnerID, text string) {
		if client == nil {
			return
		}
		if payload, err := json.Marshal(wire.ThinkingDelta(runnerID, text)); err == nil {
			client.SendEvent(client.ConnContext(), client.Running(), payload)
		}
	}))
	opts = append(opts, extraOpts...)

	mainLLM, err := cfg.deps.llms.Get(model)
	if err != nil {
		return fmt.Errorf("harness init: %w", err)
	}
	h, err := harness.New(mainLLM, opts...)
	if err != nil {
		return fmt.Errorf("harness init: %w", err)
	}
	defer h.Shutdown()

	// Machine-readable readiness line: startup worked, dialing next. The
	// only protocol line on stdout in connect mode (logs go to the log
	// file), giving the control plane crash-before-dial visibility.
	ready := struct {
		Type string `json:"type"`
		PID  int    `json:"pid"`
		Mode string `json:"mode"`
		URL  string `json:"connect_url"`
	}{"ready", os.Getpid(), "connect", cfg.ConnectURL}
	if b, err := json.Marshal(ready); err == nil {
		fmt.Println(string(b))
	}

	// One shared per-process turn tally: ToolDenied/ToolSucceeded events
	// arrive on the bus from the loop; the running turn resets it on start
	// and reads it for its result.
	stats := newTurnStats()
	registry := approvals.NewRegistry()

	// The flush barrier: a finished turn hands the forwarder an ack and waits
	// for it, so its queued events go out (and are tallied) before its result.
	// The process ctx, not the turn's: a cancelled turn still flushes; a dead
	// forwarder (process exit) can't hang the turn.
	flushReq := make(chan chan struct{})
	flushEvents := func() {
		ack := make(chan struct{})
		select {
		case flushReq <- ack:
		case <-ctx.Done():
			return
		}
		select {
		case <-ack:
		case <-ctx.Done():
		}
	}

	client, err = wsclient.New(wsclient.Options{
		URL:     cfg.ConnectURL,
		Token:   cfg.ConnectToken,
		CWD:     cwd,
		Backoff: cfg.ConnectBackoff,
	}, wsclient.Handlers{
		// One turn at a time with FIFO follow-ups (PROTOCOL.md §4) is the
		// client's job: it calls RunTurn only when a turn actually starts.
		RunTurn: func(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport {
			return runConnectTurn(ctx, h, stats, cmd, flushEvents)
		},
		Steer: h.Steer,
		Approve: func(callID string, approved bool, glob, scope string) {
			connectApprove(cfg.BashAllow, registry, cfg.ConnectEphemeralGrants || scope == "session", callID, approved, glob)
		},
		Answer:         ask.answer,
		SetModel:       func(ref string) error { return setConnectModel(cfg, ref, h) },
		SetThinking:    h.SetThinking,
		CurrentModel:   h.GetCurrentModel,
		SupportsVision: h.SupportsVision,
		ConversationID: func() string {
			if dir, _ := h.SessionInfo(); dir == "" {
				return "" // persistence off: nothing for the plane to --resume
			}
			return h.ConversationID()
		},
		OnDisconnect: stats.reset, // the dead turn's tally dies with it
	})
	if err != nil {
		return fmt.Errorf("wsclient init: %w", err)
	}

	// Bus forwarding: mirror api/events.go's observe (approval-responder
	// capture) and push every forwardable event upstream, tagged with the
	// running turn's correlation id.
	sub := h.EventBus().Subscribe(256)
	go forwardConnectEvents(ctx, sub, client, registry, stats, flushReq)

	// Run until the process context is cancelled; Run reconnects forever on
	// transient failures and exits non-zero on fatal ones.
	return client.Run(ctx)
}

// runConnectTurn executes one turn: RunTurnWithImages with the images from
// the command, reporting the answer, error, and the turn's tally (denied
// calls, files touched). The turn context arrives already tied to connection
// liveness from the wsclient. flush runs before the tally is read, so the
// turn's late events are forwarded and counted before the result.
func runConnectTurn(ctx context.Context, h *harness.Harness, stats *turnStats, cmd *wsclient.Query, flush func()) wsclient.TurnReport {
	images := make([]common.ImageSource, len(cmd.Images))
	for i, img := range cmd.Images {
		images[i] = common.ImageSource{MediaType: img.MediaType, Data: img.Data}
	}
	stats.reset()
	answer, err := h.RunTurnWithImages(ctx, cmd.Query, images)
	flush()
	denied, files := stats.snapshot()
	return wsclient.TurnReport{Answer: answer, Err: err, Denied: denied, FilesTouched: files}
}

// turnStats is the running turn's tally, fed by the event pump: permission
// denials (result.denied_tools) and the paths of successful Read/Edit/Write
// calls (result.files_touched), deduplicated in first-seen order. Reset at
// turn start and on disconnect.
type turnStats struct {
	mu     sync.Mutex
	denied int
	files  []string
	seen   map[string]struct{}
}

func newTurnStats() *turnStats {
	return &turnStats{seen: make(map[string]struct{})}
}

func (s *turnStats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denied = 0
	s.files = nil
	s.seen = make(map[string]struct{})
}

func (s *turnStats) addDenied() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denied++
}

func (s *turnStats) addFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[path]; dup {
		return
	}
	s.seen[path] = struct{}{}
	s.files = append(s.files, path)
}

// snapshot returns the tally as a copy.
func (s *turnStats) snapshot() (denied int, files []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied, append([]string(nil), s.files...)
}

// fileTools are the builtins whose successful call touches the path in
// their input's file_path.
var fileTools = map[string]bool{"Read": true, "Edit": true, "Write": true}

// touchedPath extracts the file path from a successful file-tool call; ""
// when the tool is not a file tool or the input has no path.
func touchedPath(toolName, input string) string {
	if !fileTools[toolName] {
		return ""
	}
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil {
		return ""
	}
	return in.FilePath
}

// connectSink is the slice of *wsclient.Client the event forwarder uses.
type connectSink interface {
	Running() string
	ConnContext() context.Context
	SendEvent(ctx context.Context, turnID string, envelope json.RawMessage)
	RequestApproval(turnID, callID, tool, input string)
	Disconnect(reason string)
}

// connectBacklogCap bounds the forwarder's queue. A connected plane this far
// behind has stopped reading: the backlog is discarded and the connection
// dropped, taking the normal disconnect path. A var so tests can lower it.
var connectBacklogCap = 100_000

// forwardConnectEvents pumps bus events to the control plane until ctx is
// done or the subscription closes. It tallies the turn (denials, files
// touched), captures approval responders, and forwards events tagged with
// the running turn's id. Delivery is lossless under a slow plane: a relay
// moves ch into an unbounded eventQueue, so the bus subscription never
// fills behind a blocked SendEvent (EventBus.Emit drops on full). A request
// on flush rides the same queue as a marker, acked once every event queued
// ahead of it is forwarded: the loop emits synchronously, so a finished
// turn's events are all in ch by then and go out before its result. Events
// with no running turn (emitted between turns, process-level events) are
// dropped by the client.
func forwardConnectEvents(ctx context.Context, ch <-chan core.Event, client connectSink, registry *approvals.Registry, stats *turnStats, flush <-chan chan struct{}) {
	q := newEventQueue()
	go relayConnectEvents(ctx, ch, flush, q, client, connectBacklogCap)
	subagents := make(map[string]string)
	for {
		ev, ok := q.pop()
		if !ok {
			return
		}
		if m, ok := ev.(connectFlushMarker); ok {
			close(m.ack)
			continue
		}
		observeConnectEvent(ev, registry, subagents, stats)
		switch ev.(type) {
		case core.ModelChangedEvent, core.ThinkingChangedEvent:
			// Echoes of plane commands, emitted between turns: not turn
			// events, and they'd be tagged with whichever turn runs when
			// they're forwarded. The plane already knows what it set.
			continue
		}
		// An escalated call waits on the plane's decision, so the plane
		// has to be asked: the event envelope below only narrates it.
		if a, ok := ev.(core.ApprovalRequestedEvent); ok {
			if turn := client.Running(); turn != "" {
				client.RequestApproval(turn, a.CallID, a.ToolName, a.Input)
			}
		}
		env := wire.ToWire(ev)
		payload, err := json.Marshal(env)
		if err != nil {
			continue
		}
		if turn := client.Running(); turn != "" {
			// SendEvent blocks on the connection's context — the
			// lossless backpressure contract — so pass that, not the
			// process ctx: a dead connection unblocks the pump.
			client.SendEvent(client.ConnContext(), turn, payload)
		}
	}
}

// connectFlushMarker is a flush request queued behind the events it waits
// for; the forwarder closes ack when it pops it. Never leaves the process.
type connectFlushMarker struct {
	core.BaseEvent
	ack chan struct{}
}

// relayConnectEvents moves bus events into q without ever blocking on the
// plane, and turns flush requests into queue markers after draining what ch
// already holds. A backlog past limit is discarded and the connection
// dropped. It closes q when ctx is done or ch closes, ending the
// forwarder once the backlog is out.
func relayConnectEvents(ctx context.Context, ch <-chan core.Event, flush <-chan chan struct{}, q *eventQueue, client connectSink, limit int) {
	defer q.close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			q.push(ev)
		case ack := <-flush:
			open := drainConnectEvents(ch, q)
			q.push(connectFlushMarker{ack: ack})
			if !open {
				return
			}
		}
		if q.len() > limit {
			discardConnectBacklog(q)
			client.Disconnect(fmt.Sprintf("event backlog over %d; plane not reading", limit))
		}
	}
}

// discardConnectBacklog drops every queued event, releasing any flush
// waiting in it so its turn can report.
func discardConnectBacklog(q *eventQueue) {
	for _, ev := range q.drain() {
		if m, ok := ev.(connectFlushMarker); ok {
			close(m.ack)
		}
	}
}

// drainConnectEvents pushes every event currently buffered in ch onto q;
// false when ch is closed.
func drainConnectEvents(ch <-chan core.Event, q *eventQueue) bool {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			q.push(ev)
		default:
			return true
		}
	}
}

// observeConnectEvent applies one event's bookkeeping side effects — the
// connect-mode analogue of api/events.go's observe.
func observeConnectEvent(ev core.Event, registry *approvals.Registry, subagents map[string]string, stats *turnStats) {
	switch e := ev.(type) {
	case core.ApprovalRequestedEvent:
		registry.Add(e.CallID, approvals.Pending{Respond: e.Respond, Tool: e.ToolName, Input: e.Input})
	case core.SubagentStartedEvent:
		subagents[e.RunnerID] = e.AgentID
	case core.SubagentStoppedEvent:
		delete(subagents, e.RunnerID)
	case core.ToolDeniedEvent:
		stats.addDenied()
	case core.ToolSucceededEvent:
		if p := touchedPath(e.ToolName, e.Input); p != "" {
			stats.addFile(p)
		}
	}
}

// connectApprove answers a pending approval and applies an allow glob: in
// memory for the process lifetime (ephemeral — the fleet default, so
// per-node grants die with the node, and also what an explicit
// scope:"session" asks for) or through the settings store (durable,
// serve-mode parity) when connect.ephemeral_grants is false and the plane
// did not ask for session scope.
func connectApprove(store *app.BashAllowStore, registry *approvals.Registry, ephemeral bool, callID string, approved bool, glob string) {
	if approved && glob != "" && store != nil {
		if ephemeral {
			store.Rules().AllowPatternSession(glob)
		} else if err := store.Add(glob); err != nil {
			slog.Warn("connect: persist allow rule failed", "glob", glob, "error", err)
		}
	}
	answerApproval(registry, callID, approved)
}

// answerApproval answers a pending approval; unknown call ids are no-ops
// (the request timed out on the harness side).
func answerApproval(registry *approvals.Registry, callID string, approved bool) {
	p, ok := registry.Take(callID)
	if !ok {
		return
	}
	p.Respond(approved)
}

// setConnectModel validates a model ref through the registry, builds the
// client, and switches the harness (same validation as POST /model).
func setConnectModel(cfg *cliConfig, ref string, h *harness.Harness) error {
	rm, err := cfg.deps.resolve(ref)
	if err != nil {
		return err
	}
	llm, err := cfg.deps.llms.Get(rm)
	if err != nil {
		return err
	}
	return h.SetLLM(llm)
}
