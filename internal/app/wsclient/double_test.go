package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// planeDouble is an in-repo control-plane test double: an httptest server
// accepting the upgrade with the tenzing.v1 subprotocol, reading hello,
// answering welcome, and then running an optional per-connection script on
// the SAME connection the client reads — commands sent anywhere else never
// reach the client. Lives in test files only, per the plan.
type planeDouble struct {
	srv *httptest.Server

	mu         sync.Mutex
	hello      Hello // last hello received
	welcomeID  string
	script     func(pc *planeConn)
	lastResult map[string]any // scripted tests record results here
	runningOf  func() string  // client probe, installed by newTestClient

	// onHello can replace the default welcome (e.g. refuse the version).
	onHello func(c *websocket.Conn, h Hello)
}

// newPlaneDouble starts the double; t closes it.
func newPlaneDouble(t *testing.T) *planeDouble {
	t.Helper()
	p := &planeDouble{welcomeID: "agent-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/fleet", p.serve)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// withScript sets the per-connection script; call before the client dials.
func (p *planeDouble) withScript(script func(pc *planeConn)) *planeDouble {
	p.script = script
	return p
}

// url is the double's ws:// endpoint.
func (p *planeDouble) url() string {
	return "ws" + strings.TrimPrefix(p.srv.URL, "http") + "/fleet"
}

// serve upgrades and runs the scripted session.
func (p *planeDouble) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()

	// Read hello.
	_, data, err := c.Read(ctx)
	if err != nil {
		return
	}
	var h Hello
	if err := json.Unmarshal(data, &h); err != nil {
		return
	}
	p.mu.Lock()
	p.hello = h
	onHello, welcomeID := p.onHello, p.welcomeID
	p.mu.Unlock()

	if onHello != nil {
		onHello(c, h)
		return
	}
	wb, _ := json.Marshal(Welcome{Type: "welcome", AgentID: welcomeID})
	if err := c.Write(ctx, websocket.MessageText, wb); err != nil {
		return
	}

	if script := p.script; script != nil {
		script(&planeConn{conn: c, ctx: ctx, p: p})
		return
	}

	// Default: drain agent→plane traffic until close.
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

// planeConn is the scripted plane-side handle for one client connection.
type planeConn struct {
	conn *websocket.Conn
	ctx  context.Context
	p    *planeDouble
}

// send writes one plane→agent command on the client's connection.
func (pc *planeConn) send(v any) {
	b, _ := json.Marshal(v)
	_ = pc.conn.Write(pc.ctx, websocket.MessageText, b)
}

// read reads the next agent→plane message into m (blocking; logs failures).
func (pc *planeConn) read(t *testing.T, m any) {
	t.Helper()
	_, data, err := pc.conn.Read(pc.ctx)
	if err != nil {
		t.Logf("plane read: %v", err)
		return
	}
	if err := json.Unmarshal(data, m); err != nil {
		t.Logf("plane: non-JSON upstream: %s", data)
	}
}

// runningID exposes the client's running-turn id via the double (scripts
// wait on it without racing the client's internal state). The probe is
// stored per-double, guarded by p.mu, so concurrent tests never share it.
func (p *planeDouble) runningID() string {
	p.mu.Lock()
	f := p.runningOf
	p.mu.Unlock()
	if f == nil {
		return ""
	}
	return f()
}

// setRunningOf installs the client probe for this double.
func (p *planeDouble) setRunningOf(f func() string) {
	p.mu.Lock()
	p.runningOf = f
	p.mu.Unlock()
}

// testT adapts a *testing.T for scripted reads. Scripts run on the double's
// handler goroutine; a panic there fails the test with the stack.
func testT(t *testing.T) *testing.T { return t }

// newTestClient builds a client over the double with instant backoff and
// seeded jitter for deterministic tests, wiring the runningOf probe.
func newTestClient(t *testing.T, p *planeDouble, h Handlers, mutate func(*Options)) *Client {
	t.Helper()
	opts := Options{
		URL:     p.url(),
		CWD:     "/tmp/work",
		Backoff: time.Millisecond,
		Random:  rand.New(rand.NewSource(1)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := New(opts, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.setRunningOf(c.Running)
	return c
}

// runClient runs the client until ctx is cancelled.
func runClient(ctx context.Context, c *Client) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return done
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// waitForOK polls cond until true or the timeout elapses, returning false
// instead of failing — for use inside scripted goroutines where t.Fatalf is
// forbidden (non-test goroutine). The test asserts the outcome after joining.
func waitForOK(what string, cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// handlersFor builds a minimal Handlers with recording fakes.
func handlersFor(t *testing.T) (Handlers, *handlerRecorder) {
	t.Helper()
	rec := &handlerRecorder{}
	return Handlers{
		RunTurn: func(ctx context.Context, cmd *Query) (string, error, int) {
			rec.runCalled(cmd)
			<-ctx.Done() // hold the turn until cancelled, by default
			return "", ctx.Err(), 0
		},
		Steer:   func(message string) error { rec.steerCalled(message); return nil },
		Cancel:  func() { rec.cancelCalled() },
		Approve: func(callID string, approved bool, glob string) { rec.approveCalled(callID, approved, glob) },
		SetModel: func(model string) error {
			rec.setModelCalled(model)
			return errors.New("model " + model + " not declared")
		},
		SetThinking:    func(enabled bool) error { rec.thinkingCalled(enabled); return nil },
		CurrentModel:   func() string { return "glm-5.3" },
		SupportsVision: func() bool { return true },
	}, rec
}

// handlerRecorder captures handler calls for assertions.
type handlerRecorder struct {
	mu        sync.Mutex
	queries   []Query
	steers    []string
	cancels   int
	setModels []string
	thinkings []bool
	approves  []struct {
		callID   string
		approved bool
		glob     string
	}
}

func (r *handlerRecorder) runCalled(cmd *Query) {
	r.mu.Lock()
	r.queries = append(r.queries, *cmd)
	r.mu.Unlock()
}

func (r *handlerRecorder) steerCalled(m string) {
	r.mu.Lock()
	r.steers = append(r.steers, m)
	r.mu.Unlock()
}

func (r *handlerRecorder) cancelCalled() {
	r.mu.Lock()
	r.cancels++
	r.mu.Unlock()
}

func (r *handlerRecorder) setModelCalled(m string) {
	r.mu.Lock()
	r.setModels = append(r.setModels, m)
	r.mu.Unlock()
}

func (r *handlerRecorder) thinkingCalled(b bool) {
	r.mu.Lock()
	r.thinkings = append(r.thinkings, b)
	r.mu.Unlock()
}

func (r *handlerRecorder) approveCalled(callID string, approved bool, glob string) {
	r.mu.Lock()
	r.approves = append(r.approves, struct {
		callID   string
		approved bool
		glob     string
	}{callID, approved, glob})
	r.mu.Unlock()
}
