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
// stops forwarding that turn's events.
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

	// The serial queue: one turn at a time with FIFO follow-ups (PROTOCOL.md
	// §4). The wsclient's RunTurn handler funnels through it, so a query
	// arriving mid-turn waits for the next slot; disconnect flushes waiters.
	serial := newConnectSerialQueue(func(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport {
		return runConnectTurn(ctx, h, stats, cmd)
	})

	client, err := wsclient.New(wsclient.Options{
		URL:     cfg.ConnectURL,
		Token:   cfg.ConnectToken,
		CWD:     cwd,
		Backoff: cfg.ConnectBackoff,
	}, wsclient.Handlers{
		RunTurn: serial.run,
		Steer:   h.Steer,
		Cancel:  serial.flush, // the explicit cancel: flush waiters (the running turn is cancelled by the client's cancel command path)
		Approve: func(callID string, approved bool, glob string) {
			connectApprove(cfg.BashAllow, registry, cfg.ConnectEphemeralGrants, callID, approved, glob)
		},
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
		OnDisconnect: func() {
			serial.flush() // waiters give up; the plane infers from the disconnect
			stats.reset()  // the dead turn's tally dies with it
		},
		OnReconnect: serial.reset, // accept fresh queries on the new connection
	})
	if err != nil {
		return fmt.Errorf("wsclient init: %w", err)
	}

	// Bus forwarding: mirror api/events.go's observe (approval-responder
	// capture) and push every forwardable event upstream, tagged with the
	// running turn's correlation id.
	sub := h.EventBus().Subscribe(256)
	go forwardConnectEvents(ctx, sub, client, registry, stats)

	// Run until the process context is cancelled; Run reconnects forever on
	// transient failures and exits non-zero on fatal ones.
	return client.Run(ctx)
}

// connectRunFunc executes one turn on ctx and reports its outcome.
type connectRunFunc func(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport

// connectSerialQueue serializes turn execution with FIFO follow-ups. The
// wsclient calls run from executeQuery (one goroutine per accepted query);
// waiters park until it is their turn. Semantics, per PROTOCOL.md §4 and the
// advisor flags: a `cancel` command flushes waiters (they report outcome
// "cancelled" — serve-mode parity: cancelling wants the agent to stop, not
// to watch the queue start the next turn); a disconnect does the same (the
// plane infers from the disconnect; held queries would run unobserved after
// reconnect — flush, don't carry).
type connectSerialQueue struct {
	runFn connectRunFunc

	mu      sync.Mutex
	cv      *sync.Cond // broadcast on slot release and on flush
	running bool
	flushed bool // cancel/disconnect: waiters give up instead of starting
}

// newConnectSerialQueue builds an idle queue over runFn.
func newConnectSerialQueue(runFn connectRunFunc) *connectSerialQueue {
	q := &connectSerialQueue{runFn: runFn}
	q.cv = sync.NewCond(&q.mu)
	return q
}

// run executes cmd now if idle, or parks it FIFO until its turn comes.
// It blocks until this cmd's turn finishes, so the caller (the wsclient's
// per-query goroutine) can report the result.
func (q *connectSerialQueue) run(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport {
	q.mu.Lock()
	for q.running && !q.flushed {
		q.cv.Wait()
	}
	if q.flushed {
		q.mu.Unlock()
		return wsclient.TurnReport{Err: context.Canceled}
	}
	q.running = true
	q.mu.Unlock()

	defer func() {
		q.mu.Lock()
		q.running = false
		q.cv.Broadcast()
		q.mu.Unlock()
	}()
	return q.runFn(ctx, cmd)
}

// flush marks the queue flushed and wakes every waiter; the slot owner
// (if any) is cancelled by the caller of flush (wsclient cancelInFlight for
// disconnects, the explicit cancel command for user cancels).
func (q *connectSerialQueue) flush() {
	q.mu.Lock()
	q.flushed = true
	q.cv.Broadcast()
	q.mu.Unlock()
}

// reset clears the flush marker after a disconnect so the reconnected
// connection accepts fresh queries. Only the disconnect path calls it.
func (q *connectSerialQueue) reset() {
	q.mu.Lock()
	q.flushed = false
	q.mu.Unlock()
}

// parked reports how many goroutines are waiting for a slot (test probe;
// with one waiter at most in practice, 0 or 1).
func (q *connectSerialQueue) parked() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running {
		return 1
	}
	return 0
}

// runConnectTurn executes one turn: RunTurnWithImages with the images from
// the command, reporting the answer, error, and the turn's tally (denied
// calls, files touched). The turn context arrives already tied to connection
// liveness from the wsclient.
func runConnectTurn(ctx context.Context, h *harness.Harness, stats *turnStats, cmd *wsclient.Query) wsclient.TurnReport {
	images := make([]common.ImageSource, len(cmd.Images))
	for i, img := range cmd.Images {
		images[i] = common.ImageSource{MediaType: img.MediaType, Data: img.Data}
	}
	stats.reset()
	answer, err := h.RunTurnWithImages(ctx, cmd.Query, images)
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

// forwardConnectEvents pumps bus events to the control plane until ctx is
// done or the subscription closes. It tallies the turn (denials, files
// touched), captures approval responders, and forwards events tagged with
// the running turn's id — events with no running turn (teardown stragglers,
// process-level events) are dropped by the client.
func forwardConnectEvents(ctx context.Context, ch <-chan core.Event, client *wsclient.Client, registry *approvals.Registry, stats *turnStats) {
	subagents := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			observeConnectEvent(ev, registry, subagents, stats)
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

// connectApprove answers a pending approval and applies an "allow always"
// glob: in memory for the process lifetime (ephemeral, the fleet default —
// per-node grants die with the node) or through the settings store
// (durable, serve-mode parity) when connect.ephemeral_grants is false.
func connectApprove(store *app.BashAllowStore, registry *approvals.Registry, ephemeral bool, callID string, approved bool, glob string) {
	if approved && glob != "" && store != nil {
		if ephemeral {
			store.Rules().AllowPattern(glob)
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
