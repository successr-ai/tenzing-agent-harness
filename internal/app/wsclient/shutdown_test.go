package wsclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// readAll records every agent→plane message the script reads until the
// connection closes, so shutdown tests can assert the ordering
// result → shutdown_ack.
func recordUpstream(pc *planeConn, t *testing.T) {
	for {
		var m map[string]any
		_, data, err := pc.conn.Read(pc.ctx)
		if err != nil {
			return
		}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Logf("plane: non-JSON upstream: %s", data)
			continue
		}
		pc.p.mu.Lock()
		pc.p.upstream = append(pc.p.upstream, m)
		pc.p.mu.Unlock()
	}
}

// TestShutdownMidTurn: shutdown during a turn cancels it, the cancelled
// result precedes shutdown_ack, Run returns nil, and no reconnect follows.
func TestShutdownMidTurn(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long"})
		waitForOK("turn started", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Shutdown{Type: "shutdown", ID: "s1"})
		recordUpstream(pc, t)
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runClient(ctx, c)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}

	p.mu.Lock()
	msgs := append([]map[string]any(nil), p.upstream...)
	conns := p.connections
	p.mu.Unlock()
	if len(msgs) != 2 {
		t.Fatalf("upstream after shutdown = %v, want [result, shutdown_ack]", msgs)
	}
	if msgs[0]["type"] != "result" || msgs[0]["id"] != "q1" || msgs[0]["outcome"] != "cancelled" {
		t.Errorf("first message = %v, want result q1 cancelled", msgs[0])
	}
	if msgs[1]["type"] != "shutdown_ack" || msgs[1]["id"] != "s1" {
		t.Errorf("second message = %v, want shutdown_ack s1", msgs[1])
	}
	if conns != 1 {
		t.Errorf("connections = %d, want 1 (no reconnect after shutdown)", conns)
	}
	rec.mu.Lock()
	cancels := rec.cancels
	rec.mu.Unlock()
	if cancels != 1 {
		t.Errorf("Cancel handler calls = %d, want 1", cancels)
	}
}

// TestShutdownIdle: shutdown with no turn running acks immediately and
// Run returns nil.
func TestShutdownIdle(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Shutdown{Type: "shutdown", ID: "s2"})
		recordUpstream(pc, t)
	})
	h, _ := handlersFor(t)
	c := newTestClient(t, p, h, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runClient(ctx, c)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	p.mu.Lock()
	msgs := append([]map[string]any(nil), p.upstream...)
	p.mu.Unlock()
	if len(msgs) != 1 || msgs[0]["type"] != "shutdown_ack" || msgs[0]["id"] != "s2" {
		t.Errorf("upstream = %v, want [shutdown_ack s2]", msgs)
	}
}
