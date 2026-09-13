package wsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// FatalError is a startup-shaped failure: the process must exit non-zero
// instead of retrying (config errors, protocol handshake failures that
// persist). Everything else is transient.
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

// maxBackoff caps the reconnect delay.
const maxBackoff = 30 * time.Second

// Handlers is what the connect-mode wiring provides so the client can drive
// the harness without importing it. The client owns the connection lifecycle
// and cancels turns via per-turn contexts derived from connection liveness.
type Handlers struct {
	// RunTurn executes one turn to completion on ctx. The client calls it
	// with ctx already tied to connection liveness: when the socket drops,
	// the context cancels and the harness turn aborts (loop.go's clean
	// "turn canceled" outcome). The client builds the Result from the
	// return values.
	RunTurn func(ctx context.Context, cmd *Query) (answer string, err error, denied int)
	// Steer injects mid-turn input.
	Steer func(message string) error
	// Cancel stops the running turn (and drops queued ones).
	Cancel func()
	// Approve answers a pending approval call.
	Approve func(callID string, approved bool, glob string)
	// SetModel switches the main model; error feeds back to the plane.
	SetModel func(model string) error
	// SetThinking toggles reasoning.
	SetThinking func(enabled bool) error
	// CurrentModel reports the wire name of the active main model.
	CurrentModel func() string
	// SupportsVision reports whether the active model accepts images.
	SupportsVision func() bool
	// OnDisconnect fires once per connection loss, after the in-flight
	// turn has been cancelled. Optional.
	OnDisconnect func()
	// OnReconnect fires after a successful handshake on a new connection.
	// Optional.
	OnReconnect func()
}

// Options configures a Client.
type Options struct {
	// URL is the control plane's ws:// or wss:// endpoint.
	URL string
	// Token is the bearer token on the upgrade request; "" = none.
	Token string
	// CWD is reported in hello.
	CWD string
	// Backoff is the reconnect delay base; 0 = 1s. Doubles to a 30s cap,
	// full jitter.
	Backoff time.Duration
	// Random is the jitter source; nil = seeded from the clock.
	Random *rand.Rand
	// DialTimeout bounds one dial attempt; 0 = 10s.
	DialTimeout time.Duration
}

// Client is one agent's control-plane connection. Construct with New, run
// with Run.
type Client struct {
	opts Options
	h    Handlers

	// mu guards turn bookkeeping below.
	mu         sync.Mutex
	cancelTurn context.CancelFunc // non-nil while a turn is running
	currentID  string             // correlation id of the running turn
	connLost   chan struct{}      // closed by cancelInFlight; per-turn watcher
	lastTurn   *LastTurn          // reported on the next hello
	outbox     chan []byte        // upstream messages awaiting the write pump
	connCtx    connCtxHolder      // the live connection's context (for SendEvent)

	dialTimeout time.Duration
}

// New builds a client; all Handlers must be non-nil.
func New(opts Options, h Handlers) (*Client, error) {
	if h.RunTurn == nil || h.Cancel == nil || h.Approve == nil || h.Steer == nil ||
		h.SetModel == nil || h.SetThinking == nil || h.CurrentModel == nil || h.SupportsVision == nil {
		return nil, fmt.Errorf("wsclient: all Handlers must be non-nil")
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.Random == nil {
		opts.Random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	return &Client{
		opts:        opts,
		h:           h,
		outbox:      make(chan []byte, 1024),
		dialTimeout: opts.DialTimeout,
	}, nil
}

// hello builds the registration message, including lastTurn when set.
func (c *Client) hello() Hello {
	c.mu.Lock()
	lt := c.lastTurn
	c.mu.Unlock()
	h := Hello{
		Type:     "hello",
		Protocol: ProtocolVersion,
		PID:      os.Getpid(),
		CWD:      c.opts.CWD,
		Model:    c.h.CurrentModel(),
		Capabilities: Capabilities{
			Vision:    c.h.SupportsVision(),
			Approvals: true,
		},
		LastTurn: lt,
	}
	return h
}

// send queues one upstream message for the write pump. It gives up once the
// process context is done (the outbox would otherwise hold a queued result
// forever after the connection died with no writer to drain it).
func (c *Client) send(ctx context.Context, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshal failure is a bug, not a runtime condition; drop loudly.
		slog.Error("wsclient: marshal upstream message", "type", fmt.Sprintf("%T", v), "error", err)
		return
	}
	select {
	case c.outbox <- b:
	case <-ctx.Done():
		slog.Warn("wsclient: dropping upstream message; process shutting down", "type", fmt.Sprintf("%T", v))
	}
}

// SendEvent forwards one harness event upstream, tagged with the running
// turn's correlation id. Called by the cmd/app wiring's event pump; the
// envelope is raw wire JSON (already encoded there). The send blocks — the
// outbox is the lossless buffer per PROTOCOL.md §4's backpressure contract:
// a slow control plane stalls the wiring pump (and the bus behind it),
// never a dropped event. It gives up only when ctx (the connection context)
// ends — a dead connection's backlog is discarded by design.
func (c *Client) SendEvent(ctx context.Context, turnID string, envelope json.RawMessage) {
	if c.Running() == "" {
		return
	}
	b, err := json.Marshal(Event{Type: "event", ID: turnID, Envelope: envelope})
	if err != nil {
		slog.Error("wsclient: marshal event", "error", err)
		return
	}
	select {
	case c.outbox <- b:
	case <-ctx.Done():
		slog.Warn("wsclient: dropping event; connection gone", "turn", turnID)
	}
}

// Running reports the correlation id of the running turn, "" when idle.
func (c *Client) Running() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentID
}

// connCtx holds the current connection's context, so the wiring's event
// pump can block on the same liveness the write pump has (a dead connection
// unblocks SendEvent's lossless send). Swapped per connection under mu.
type connCtxHolder struct {
	mu  sync.Mutex
	ctx context.Context
}

func (h *connCtxHolder) get() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

func (h *connCtxHolder) set(ctx context.Context) {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
}

// ConnContext reports the live connection's context, for the wiring's event
// pump: SendEvent blocks on it, so a dead connection unblocks the lossless
// send. Background when disconnected.
func (c *Client) ConnContext() context.Context {
	return c.connCtx.get()
}

// authHeader builds the bearer-token header for the upgrade request.
func authHeader(token string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+token)
	return h
}

// handshake sends hello and waits for welcome. A protocol-version refusal or
// a malformed handshake is transient (retry with backoff); nothing here is
// fatal — the spec makes the agent keep dialing until the plane upgrades.
func (c *Client) handshake(ctx context.Context, conn *websocket.Conn) error {
	hb, err := json.Marshal(c.hello())
	if err != nil {
		return fmt.Errorf("marshal hello: %w", err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if err := conn.Write(wctx, websocket.MessageText, hb); err != nil {
		return fmt.Errorf("write hello: %w", err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	typ, data, err := conn.Read(rctx)
	if err != nil {
		return fmt.Errorf("read welcome: %w", err)
	}
	if typ != websocket.MessageText {
		return fmt.Errorf("welcome: expected text frame, got %v", typ)
	}
	var w Welcome
	if err := json.Unmarshal(data, &w); err != nil || w.Type != "welcome" {
		return fmt.Errorf("handshake: expected welcome, got %q", string(data))
	}
	if w.AgentID == "" {
		return fmt.Errorf("handshake: welcome without agent_id")
	}
	slog.Info("registered with control plane", "agent_id", w.AgentID)
	return nil
}

// Run dials, handshakes, and serves until ctx is cancelled or a fatal error
// occurs. Transient failures reconnect with jittered exponential backoff.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.opts.Backoff
	for {
		err := c.dialAndServe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var fatal *FatalError
		if errAs(err, &fatal) {
			return fatal
		}
		if err != nil {
			slog.Warn("control plane connection lost", "url", c.opts.URL, "error", err)
		}
		delay := jitter(backoff, c.opts.Random)
		slog.Info("reconnecting to control plane", "url", c.opts.URL, "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// errAs is errors.As, kept local so the file reads without the import noise
// of multiple small helpers.
func errAs(err error, target **FatalError) bool {
	for err != nil {
		if f, ok := err.(*FatalError); ok {
			*target = f
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// jitter returns a uniform sample of [0, d) — full jitter.
func jitter(d time.Duration, r *rand.Rand) time.Duration {
	return time.Duration(r.Int63n(int64(d) + 1))
}

// dialAndServe performs one dial → handshake → serve cycle and returns when
// the connection ends. The in-flight turn (if any) is cancelled on the way
// out, whatever the cause.
func (c *Client) dialAndServe(ctx context.Context) error {
	dctx, dcancel := context.WithTimeout(ctx, c.dialTimeout)
	defer dcancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol}}
	if c.opts.Token != "" {
		opts.HTTPHeader = authHeader(c.opts.Token)
	}
	conn, _, err := websocket.Dial(dctx, c.opts.URL, opts)
	if err != nil {
		return fmt.Errorf("dial control plane: %w", err)
	}
	// Normal closure on our own teardown; the deferred close below only
	// fires if the pumps have not already closed the connection.
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	if err := c.handshake(ctx, conn); err != nil {
		return err
	}
	slog.Info("connected to control plane", "url", c.opts.URL)
	if c.h.OnReconnect != nil {
		c.h.OnReconnect()
	}

	// connCtx is the connection's lifetime; the in-flight turn's context
	// derives from it, so a dead socket cancels the turn (the load-bearing
	// rule). connDone closes exactly once, when connCtx ends.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	c.connCtx.set(connCtx)
	defer c.connCtx.set(nil)
	connDone := make(chan struct{})
	var once sync.Once
	closeConn := func() { once.Do(func() { close(connDone) }) }
	go func() {
		<-connCtx.Done()
		closeConn()
	}()

	// Write pump: single goroutine draining the outbox at connection speed.
	go func() {
		for {
			select {
			case b := <-c.outbox:
				wctx, wcancel := context.WithTimeout(connCtx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					slog.Warn("wsclient: write failed; dropping connection", "error", err)
					connCancel()
					return
				}
			case <-connCtx.Done():
				return
			}
		}
	}()

	readErr := c.readPump(connCtx, conn)
	connCancel()
	<-connDone
	return readErr
}

// readPump decodes plane→agent messages and dispatches them until the
// connection ends. It cancels connCancel on read failure (the disconnect).
func (c *Client) readPump(connCtx context.Context, conn *websocket.Conn) error {
	for {
		typ, data, err := conn.Read(connCtx)
		if err != nil {
			// Disconnect: cancel the in-flight turn. The turn's
			// RunTurn handler sees ctx.Done and returns; its result
			// lands in the outbox (best-effort) and its backlog is
			// dropped by the cmd/app wiring's event filter.
			c.cancelInFlight()
			conn.Close(websocket.StatusGoingAway, "connection lost")
			return fmt.Errorf("read control plane: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		msg, err := decode(data)
		if err != nil {
			var unk *UnknownMessageError
			if asUnknown(err, &unk) {
				// Additive versioning: log and drop, never fatal.
				slog.Warn("wsclient: dropping unknown control-plane message", "error", err)
				continue
			}
			slog.Warn("wsclient: malformed control-plane message", "error", err)
			continue
		}
		c.dispatch(connCtx, msg)
	}
}

// asUnknown is errors.As for UnknownMessageError.
func asUnknown(err error, target **UnknownMessageError) bool {
	if u, ok := err.(*UnknownMessageError); ok {
		*target = u
		return true
	}
	return false
}

// dispatch routes one decoded command. ctx is the connection context.
func (c *Client) dispatch(ctx context.Context, msg any) {
	switch m := msg.(type) {
	case *Query:
		c.executeQuery(ctx, m)
	case *Steer:
		if err := c.h.Steer(m.Message); err != nil {
			slog.Warn("wsclient: steer failed", "error", err)
		}
	case *Cancel:
		// Cancel the running turn's context FIRST (so the turn reports the
		// clean "cancelled" outcome), then flush the queue via the handler.
		// The connLost watcher is deliberately left open: the connection
		// is alive, and the classification below keys on it.
		c.cancelTurnContext()
		c.h.Cancel()
	case *Approve:
		c.h.Approve(m.CallID, m.Approved, m.Glob)
	case *SetModel:
		if err := c.h.SetModel(m.Model); err != nil {
			c.send(ctx, Error{Type: "error", ID: m.ID, Code: "set_model", Detail: err.Error()})
		}
	case *SetThinking:
		if err := c.h.SetThinking(m.Enabled); err != nil {
			c.send(ctx, Error{Type: "error", ID: m.ID, Code: "set_thinking", Detail: err.Error()})
		}
	}
}

// executeQuery runs one turn synchronously in its own goroutine: the read
// pump must keep dispatching (steer/cancel/approve) while the turn runs.
func (c *Client) executeQuery(ctx context.Context, q *Query) {
	c.mu.Lock()
	if c.cancelTurn != nil {
		// A turn is already running; per PROTOCOL.md §4 the plane may queue,
		// but queueing is the wiring's job — reflect the running turn and
		// let the plane decide. (cmd/app keeps a queue; the client tracks
		// only the active turn.)
		c.mu.Unlock()
		slog.Warn("wsclient: query arrived while a turn is running", "id", q.ID)
		return
	}
	c.currentID = q.ID
	c.mu.Unlock()

	// connLost is closed by cancelInFlight when the connection drops, so the
	// outcome classification below can tell "explicit cancel" from
	// "disconnect" without racing the connection context's teardown.
	connLost := make(chan struct{})
	c.mu.Lock()
	c.connLost = connLost
	c.mu.Unlock()

	go func() {
		// Turn context: child of the connection context. When the socket
		// dies, this cancels and the harness turn aborts cleanly.
		turnCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancelTurn = cancel
		c.mu.Unlock()

		answer, turnErr, denied := c.h.RunTurn(turnCtx, q)

		c.mu.Lock()
		c.cancelTurn = nil
		c.currentID = ""
		connLostLocal := c.connLost // the watcher installed for this turn
		c.mu.Unlock()
		// Classify BEFORE releasing the turn context: once cancel() runs,
		// turnCtx.Err() is non-nil and a completed turn would misread as
		// cancelled.
		lost := false
		select {
		case <-connLostLocal:
			lost = true
		default:
		}
		turnWasCancelled := turnCtx.Err() != nil
		cancel() // release the turn context either way

		outcome := "completed"
		errMsg := ""
		switch {
		case turnWasCancelled && !lost:
			// Turn context died but the connection lives: an explicit
			// cancel command.
			outcome = "cancelled"
		case turnWasCancelled && lost:
			// Connection dead: record for the next hello; the result may
			// never reach the plane (advisory per PROTOCOL.md §4).
			outcome = "cancelled_disconnect"
			c.mu.Lock()
			c.lastTurn = &LastTurn{ID: q.ID, Outcome: outcome}
			c.mu.Unlock()
		case turnErr != nil:
			outcome = "error"
			errMsg = turnErr.Error()
		}
		if outcome != "cancelled_disconnect" {
			c.send(ctx, Result{Type: "result", ID: q.ID, Outcome: outcome, Answer: answer, Error: errMsg, DeniedTools: denied})
		}
	}()
}

// cancelInFlight is the disconnect path: cancel the running turn's context
// (the harness aborts with the clean "turn canceled" outcome), close the
// turn's connLost watcher (so the outcome classifies as
// cancelled_disconnect), and notify the wiring.
func (c *Client) cancelInFlight() {
	c.cancelTurnContext()
	c.mu.Lock()
	connLost := c.connLost
	c.mu.Unlock()
	if connLost != nil {
		select {
		case <-connLost:
		default:
			close(connLost)
		}
	}
	if c.h.OnDisconnect != nil {
		c.h.OnDisconnect()
	}
}

// cancelTurnContext cancels the running turn's context, if any, without
// touching the connLost watcher — the explicit-cancel-command path.
func (c *Client) cancelTurnContext() {
	c.mu.Lock()
	cancel := c.cancelTurn
	id := c.currentID
	c.mu.Unlock()
	if cancel != nil {
		slog.Info("wsclient: cancelling the in-flight turn", "turn", id)
		cancel()
	}
}
