package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestHandshakeRegistersAgent: hello → welcome; the hello carries protocol,
// cwd, model, capabilities, and no last_turn on a first connect.
func TestHandshakeRegistersAgent(t *testing.T) {
	p := newPlaneDouble(t)
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runClient(ctx, c)

	waitFor(t, "welcome round-trip", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.hello.PID != 0
	})
	p.mu.Lock()
	got := p.hello
	p.mu.Unlock()
	if got.Type != "hello" || got.Protocol != ProtocolVersion || got.CWD != "/tmp/work" || got.Model != "glm-5.3" {
		t.Errorf("hello = %+v, want protocol v1 with cwd/model", got)
	}
	if !got.Capabilities.Vision || !got.Capabilities.Approvals {
		t.Errorf("capabilities = %+v, want vision+approvals", got.Capabilities)
	}
	if got.LastTurn != nil {
		t.Errorf("first connect should not carry last_turn, got %+v", got.LastTurn)
	}
	if got.ConversationID != "conv-1" {
		t.Errorf("hello.conversation_id = %q, want conv-1", got.ConversationID)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run after cancel: %v", err)
	}
}

// TestQueryRunsTurnAndReportsResult: a query command runs the handler to
// completion and the result reaches the plane with the correlation id.
func TestQueryRunsTurnAndReportsResult(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "hello world"})
		var r map[string]any
		pc.read(testT(t), &r)
		pc.p.mu.Lock()
		pc.p.lastResult = r
		pc.p.mu.Unlock()
	})
	h, rec := handlersFor(t)
	h.RunTurn = func(ctx context.Context, cmd *Query) TurnReport {
		rec.runCalled(cmd)
		return TurnReport{Answer: "the answer", Denied: 2, FilesTouched: []string{"a.go", "b.go"}}
	}
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "result recorded", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.lastResult != nil
	})
	p.mu.Lock()
	res := p.lastResult
	p.mu.Unlock()
	if res["type"] != "result" || res["id"] != "q1" || res["outcome"] != "completed" || res["answer"] != "the answer" || res["denied_tools"].(float64) != 2 {
		t.Errorf("result = %v, want completed q1 answer=the answer denied=2", res)
	}
	if files, _ := res["files_touched"].([]any); len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Errorf("result.files_touched = %v, want [a.go b.go]", res["files_touched"])
	}

	cancel()
}

// TestSteerReachesHandlerMidTurn: the read pump keeps dispatching while a
// turn runs — steer reaches the handler without waiting for the turn.
func TestSteerReachesHandlerMidTurn(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long turn"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Steer{Type: "steer", ID: "s1", Message: "focus on X"})
		// Hold the connection open so the steer has time to land.
		time.Sleep(100 * time.Millisecond)
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "steer delivered mid-turn", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.steers) > 0 && rec.steers[0] == "focus on X"
	})
	if c.Running() != "q1" {
		t.Errorf("turn should still be running during steer, got %q", c.Running())
	}
}

// TestExplicitCancelCancelsTurn: a cancel command cancels the running
// turn's context; the client classifies "cancelled" (connection alive).
func TestExplicitCancelCancelsTurn(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long turn"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Cancel{Type: "cancel", ID: "c1"})
		var r map[string]any
		pc.read(testT(t), &r)
		pc.p.mu.Lock()
		pc.p.lastResult = r
		pc.p.mu.Unlock()
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "cancel result", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.lastResult != nil
	})
	p.mu.Lock()
	res := p.lastResult
	p.mu.Unlock()
	if res["outcome"] != "cancelled" || res["id"] != "q1" {
		t.Errorf("result = %v, want outcome=cancelled id=q1", res)
	}
	c.mu.Lock()
	lt := c.lastTurn
	c.mu.Unlock()
	if lt != nil {
		t.Errorf("explicit cancel should not record last_turn (connection alive), got %+v", lt)
	}
}

// TestDisconnectCancelsTurn is the load-bearing rule: killing the
// connection mid-turn cancels the turn context, records
// cancelled_disconnect for the next hello.
func TestDisconnectCancelsTurn(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long turn"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		// Kill the connection under the client.
		pc.conn.Close(websocket.StatusGoingAway, "plane dropped")
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "cancelled_disconnect recorded", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.lastTurn != nil
	})
	c.mu.Lock()
	lt := c.lastTurn
	c.mu.Unlock()
	if lt == nil || lt.ID != "q1" || lt.Outcome != "cancelled_disconnect" {
		t.Errorf("lastTurn = %+v, want q1/cancelled_disconnect", lt)
	}
}

// TestHelloCarriesLastTurnAfterDisconnect: the next hello after a
// disconnect mid-turn carries last_turn (the resync the plane reads).
func TestHelloCarriesLastTurnAfterDisconnect(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "doomed"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		pc.conn.Close(websocket.StatusGoingAway, "plane dropped")
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "cancelled_disconnect recorded", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.lastTurn != nil
	})

	h2 := c.hello()
	if h2.LastTurn == nil || h2.LastTurn.ID != "q1" || h2.LastTurn.Outcome != "cancelled_disconnect" {
		t.Errorf("hello().LastTurn = %+v, want q1/cancelled_disconnect", h2.LastTurn)
	}
}

// TestUnknownMessageDropped: an unrecognized message type is logged and
// dropped, never fatal (additive versioning).
func TestUnknownMessageDropped(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(map[string]any{"type": "from-the-future", "x": 1})
		// The client must still be responsive.
		pc.send(Query{Type: "query", ID: "q1", Query: "still alive"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "turn started after unknown message", func() bool { return c.Running() == "q1" })
}

// TestApproveDispatchReachesHandler: an approve command reaches the
// Approve handler with its call id and decision (the wiring-level
// responder round-trip is covered in cmd/app's connect tests).
func TestApproveDispatchReachesHandler(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Approve{Type: "approve", ID: "a1", CallID: "call-1", Approved: true})
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "approve dispatched", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.approves) > 0
	})
	rec.mu.Lock()
	got := rec.approves[0]
	rec.mu.Unlock()
	if got.callID != "call-1" || !got.approved {
		t.Errorf("approve = %+v, want call-1 approved", got)
	}
}

// TestDecodeMalformed: malformed payloads are dropped, not fatal.
func TestDecodeMalformed(t *testing.T) {
	if _, err := decode([]byte("{not json")); err == nil {
		t.Error("malformed JSON should error")
	}
	_, err := decode([]byte(`{"type":"nonsense"}`))
	if err == nil {
		t.Fatal("unknown type should error")
	}
	var unk *UnknownMessageError
	if !errors.As(err, &unk) {
		t.Errorf("error = %T, want *UnknownMessageError", err)
	}
}

// TestFatalErrorWrapping pins the fatal-error taxonomy.
func TestFatalErrorWrapping(t *testing.T) {
	f := &FatalError{Err: errors.New("bad config")}
	var target *FatalError
	if !errors.As(f, &target) || target.Err.Error() != "bad config" {
		t.Error("errors.As failed on FatalError")
	}
}

// TestJitterBounds: full jitter stays within [0, d).
func TestJitterBounds(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for range 1000 {
		got := jitter(time.Second, r)
		if got < 0 || got > time.Second {
			t.Fatalf("jitter = %v, want in [0, 1s)", got)
		}
	}
}

// TestNewRequiresAllHandlers: a nil handler is a construction error, not a
// runtime panic.
func TestNewRequiresAllHandlers(t *testing.T) {
	_, err := New(Options{URL: "ws://x"}, Handlers{})
	if err == nil {
		t.Error("New with empty Handlers should error")
	}
}

// TestEventAndResultShapes pins the upstream wire shapes the spec defines.
func TestEventAndResultShapes(t *testing.T) {
	b, err := json.Marshal(Event{Type: "event", ID: "q1", Envelope: json.RawMessage(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m["type"] != "event" || m["id"] != "q1" {
		t.Errorf("event JSON = %s, want {type:event,id:q1,...}", b)
	}
	b, _ = json.Marshal(Result{Type: "result", ID: "q1", Outcome: "cancelled_disconnect"})
	m = nil
	_ = json.Unmarshal(b, &m)
	if m["outcome"] != "cancelled_disconnect" {
		t.Errorf("result outcome = %v", m["outcome"])
	}
	if _, present := m["files_touched"]; present {
		t.Errorf("empty files_touched must be omitted, got %s", b)
	}
	b, _ = json.Marshal(Hello{Type: "hello"})
	if strings.Contains(string(b), "conversation_id") {
		t.Errorf("empty conversation_id must be omitted, got %s", b)
	}
}

// TestLargeQueryArrivesIntact: a query carries the node's directive plus
// upstream context, which outgrows the websocket library's 32 KiB default
// read limit; the frame must reach the turn whole, not drop the socket.
func TestLargeQueryArrivesIntact(t *testing.T) {
	big := strings.Repeat("x", 40_000)
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: big})
		var r map[string]any
		pc.read(testT(t), &r)
	})
	var mu sync.Mutex
	got := -1
	h, _ := handlersFor(t)
	h.RunTurn = func(ctx context.Context, cmd *Query) TurnReport {
		mu.Lock()
		got = len(cmd.Query)
		mu.Unlock()
		return TurnReport{Answer: "ok"}
	}
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "large query ran", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got == len(big)
	})
}

// TestUnexpectedAgentRefusalIsFatal: a plane that answers welcome and then
// `error{code:"unexpected_agent"}` has no launch waiting for this process
// — typically it restarted since spawning it — and never will. Redialling
// is futile, so Run returns a FatalError and the process exits instead of
// retrying for the rest of its life.
func TestUnexpectedAgentRefusalIsFatal(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Error{Type: "error", Code: "unexpected_agent", Detail: "no launch in flight for this node"})
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	select {
	case err := <-runClient(ctx, c):
		var fatal *FatalError
		if !errAs(err, &fatal) {
			t.Fatalf("Run = %v, want a FatalError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run still redialling a plane that refused this agent")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connections != 1 {
		t.Errorf("connections = %d, want 1: a refusal must not be retried", p.connections)
	}
}

// TestAnswerReachesHandler: the plane's `answer` to an input_request is
// handed to the Answer handler with the request it replies to.
func TestAnswerReachesHandler(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Answer{Type: "answer", ID: "c1", RequestID: "ask-7", Text: "only never-listed drafts"})
		<-pc.ctx.Done()
	})
	var mu sync.Mutex
	var gotID, gotText string
	h, _ := handlersFor(t)
	h.Answer = func(requestID, text string) {
		mu.Lock()
		defer mu.Unlock()
		gotID, gotText = requestID, text
	}
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "answer handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotID == "ask-7" && gotText == "only never-listed drafts"
	})
}

// TestInputAndApprovalRequestsReachThePlane: the two agent→plane requests
// a turn can block on go out as their own frames, tagged with the turn.
func TestInputAndApprovalRequestsReachThePlane(t *testing.T) {
	got := make(chan map[string]any, 2)
	ready := make(chan struct{})
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		close(ready) // handshake done: the client has a live connection
		for i := 0; i < 2; i++ {
			var m map[string]any
			pc.read(testT(t), &m)
			got <- m
		}
		<-pc.ctx.Done()
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)
	<-ready

	c.RequestInput("q1", "ask-1", "Which drafts?")
	c.RequestApproval("q1", "call-1", "bash", `{"command":"rm x"}`)

	first, second := <-got, <-got
	if first["type"] != "input_request" || first["id"] != "ask-1" || first["turn_id"] != "q1" || first["question"] != "Which drafts?" {
		t.Errorf("input frame = %v", first)
	}
	if second["type"] != "approval_request" || second["id"] != "call-1" || second["tool"] != "bash" || second["turn_id"] != "q1" {
		t.Errorf("approval frame = %v", second)
	}
}
