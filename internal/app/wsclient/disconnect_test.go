package wsclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestReconnectHelloWaitsForCancelledTurn: a turn takes a moment to wind
// down after its connection drops. The next hello must still report it,
// even when the reconnect delay is near zero (full jitter can pick ~0).
func TestReconnectHelloWaitsForCancelledTurn(t *testing.T) {
	p := newPlaneDouble(t)
	p.withScript(func(pc *planeConn) {
		p.mu.Lock()
		first := p.connections == 1
		p.mu.Unlock()
		if !first {
			<-pc.ctx.Done()
			return
		}
		pc.send(Query{Type: "query", ID: "q1", Query: "doomed"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		pc.conn.Close(websocket.StatusGoingAway, "plane dropped")
	})
	h, _ := handlersFor(t)
	h.RunTurn = func(ctx context.Context, _ *Query) TurnReport {
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond) // a real turn's teardown
		return TurnReport{Err: ctx.Err()}
	}
	c := newTestClient(t, p, h, nil) // 1ms backoff
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "reconnect", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.connections == 2
	})
	p.mu.Lock()
	lt := p.hello.LastTurn
	p.mu.Unlock()
	if lt == nil || lt.ID != "q1" || lt.Outcome != "cancelled_disconnect" {
		t.Errorf("reconnect hello last_turn = %+v, want q1/cancelled_disconnect", lt)
	}
}

// TestLostTurnSendsNothing: once a turn's connection is lost, its leftover
// events, approval and input requests are discarded — never sent on a
// newer connection (PROTOCOL.md §4: the backlog is discarded).
func TestLostTurnSendsNothing(t *testing.T) {
	h, _ := handlersFor(t)
	c, err := New(Options{URL: "ws://unused"}, h)
	if err != nil {
		t.Fatal(err)
	}
	// A reconnected connection, while the old turn q1 is still winding down.
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	c.connCtx.set(connCtx, connCancel)
	lost := make(chan struct{})
	close(lost)
	c.mu.Lock()
	c.currentID = "q1"
	c.connLost = lost
	c.mu.Unlock()

	c.SendEvent(c.ConnContext(), "q1", json.RawMessage(`{"v":1}`))
	c.RequestApproval("q1", "call-1", "bash", "ls")
	c.RequestInput("q1", "req-1", "sure?")
	select {
	case b := <-c.outbox:
		t.Errorf("lost turn sent %s", b)
	default:
	}
}
