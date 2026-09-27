package wsclient

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// queryIDs returns the ids RunTurn has been called with, in order.
func (r *handlerRecorder) queryIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, len(r.queries))
	for i, q := range r.queries {
		ids[i] = q.ID
	}
	return ids
}

// resultsOf picks the result messages out of the recorded upstream.
func resultsOf(p *planeDouble) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, m := range p.upstream {
		if m["type"] == "result" {
			out = append(out, m)
		}
	}
	return out
}

// wantResults asserts the result sequence as id:outcome pairs.
func wantResults(t *testing.T, got []map[string]any, want ...string) {
	t.Helper()
	pairs := make([]string, len(got))
	for i, m := range got {
		pairs[i] = m["id"].(string) + ":" + m["outcome"].(string)
	}
	if !slices.Equal(pairs, want) {
		t.Errorf("results = %v, want %v", pairs, want)
	}
}

// TestQueryDuringTurnQueuesFIFO: a query arriving mid-turn waits for the
// running turn, then runs; each gets its own result, in order.
func TestQueryDuringTurnQueuesFIFO(t *testing.T) {
	release := make(chan struct{})
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "first"})
		waitForOK("q1 running", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Query{Type: "query", ID: "q2", Query: "second"})
		pc.send(Steer{Type: "steer", ID: "s1", Message: "marker"}) // dispatched after q2
		recordUpstream(pc, t)
	})
	h, rec := handlersFor(t)
	h.RunTurn = func(ctx context.Context, cmd *Query) TurnReport {
		rec.runCalled(cmd)
		if cmd.ID == "q1" {
			<-release
		}
		return TurnReport{Answer: cmd.ID}
	}
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "q2 dispatched", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.steers) == 1
	})
	if ids := rec.queryIDs(); !slices.Equal(ids, []string{"q1"}) {
		t.Fatalf("while q1 runs, RunTurn calls = %v, want [q1]", ids)
	}
	close(release)
	waitFor(t, "both results", func() bool { return len(resultsOf(p)) == 2 })
	wantResults(t, resultsOf(p), "q1:completed", "q2:completed")
	if ids := rec.queryIDs(); !slices.Equal(ids, []string{"q1", "q2"}) {
		t.Errorf("RunTurn calls = %v, want [q1 q2]", ids)
	}
}

// TestCancelFlushesQueuedQueries: cancel stops the running turn and drops
// the queued ones, each reporting "cancelled" after the running turn's
// result; a query sent afterwards runs normally.
func TestCancelFlushesQueuedQueries(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long"})
		waitForOK("q1 running", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Query{Type: "query", ID: "q2", Query: "queued"})
		pc.send(Cancel{Type: "cancel", ID: "c1"})
		go func() {
			waitForOK("flush results", func() bool { return len(resultsOf(pc.p)) == 2 })
			pc.send(Query{Type: "query", ID: "q3", Query: "fresh"})
		}()
		recordUpstream(pc, t)
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "q3 running", func() bool { return len(rec.queryIDs()) == 2 })
	wantResults(t, resultsOf(p), "q1:cancelled", "q2:cancelled")
	if ids := rec.queryIDs(); !slices.Equal(ids, []string{"q1", "q3"}) {
		t.Errorf("RunTurn calls = %v, want [q1 q3]", ids)
	}
}

// TestShutdownFlushesQueuedQueries: shutdown reports the running and the
// queued turns cancelled, in order, before shutdown_ack.
func TestShutdownFlushesQueuedQueries(t *testing.T) {
	p := newPlaneDouble(t).withScript(func(pc *planeConn) {
		pc.send(Query{Type: "query", ID: "q1", Query: "long"})
		waitForOK("q1 running", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Query{Type: "query", ID: "q2", Query: "queued"})
		pc.send(Shutdown{Type: "shutdown", ID: "s1"})
		recordUpstream(pc, t)
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	select {
	case err := <-runClient(ctx, c):
		if err != nil {
			t.Fatalf("Run after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}

	// Run returning doesn't mean the plane has recorded everything yet.
	waitFor(t, "result, result, shutdown_ack", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.upstream) >= 3
	})
	p.mu.Lock()
	msgs := append([]map[string]any(nil), p.upstream...)
	p.mu.Unlock()
	if len(msgs) != 3 || msgs[2]["type"] != "shutdown_ack" {
		t.Fatalf("upstream = %v, want [result q1, result q2, shutdown_ack]", msgs)
	}
	wantResults(t, resultsOf(p), "q1:cancelled", "q2:cancelled")
	if ids := rec.queryIDs(); !slices.Equal(ids, []string{"q1"}) {
		t.Errorf("RunTurn calls = %v, want [q1]", ids)
	}
}

// TestDisconnectDropsQueuedQueries: a dropped connection discards the
// queue (the plane infers from the disconnect); nothing queued on the dead
// connection runs after the reconnect.
func TestDisconnectDropsQueuedQueries(t *testing.T) {
	p := newPlaneDouble(t)
	p.withScript(func(pc *planeConn) {
		p.mu.Lock()
		first := p.connections == 1
		p.mu.Unlock()
		if !first {
			<-pc.ctx.Done() // reconnected: stay idle
			return
		}
		pc.send(Query{Type: "query", ID: "q1", Query: "long"})
		waitForOK("q1 running", func() bool { return pc.p.runningID() == "q1" })
		pc.send(Query{Type: "query", ID: "q2", Query: "queued"})
		pc.send(Steer{Type: "steer", ID: "s1", Message: "marker"}) // q2 dispatched before this
		time.Sleep(20 * time.Millisecond)
		pc.conn.Close(websocket.StatusGoingAway, "plane gone")
	})
	h, rec := handlersFor(t)
	c := newTestClient(t, p, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runClient(ctx, c)

	waitFor(t, "reconnect", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.connections == 2
	})
	waitFor(t, "q1 torn down", func() bool { return c.Running() == "" })
	time.Sleep(50 * time.Millisecond) // give a wrongly carried q2 time to start
	if ids := rec.queryIDs(); !slices.Equal(ids, []string{"q1"}) {
		t.Errorf("RunTurn calls = %v, want [q1] (q2 must not survive the disconnect)", ids)
	}
}

// TestQueryAfterShutdownNotRun: a query arriving once shutdown has begun
// never starts a turn; it reports "cancelled" (one result per query).
func TestQueryAfterShutdownNotRun(t *testing.T) {
	h, rec := handlersFor(t)
	c, err := New(Options{URL: "ws://unused"}, h)
	if err != nil {
		t.Fatal(err)
	}
	c.shuttingDown.Store(true)
	c.executeQuery(context.Background(), &Query{ID: "late"})

	select {
	case b := <-c.outbox:
		if got := string(b); got != `{"type":"result","id":"late","outcome":"cancelled","denied_tools":0}` {
			t.Errorf("reply = %s, want a cancelled result for late", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result for the late query")
	}
	if ids := rec.queryIDs(); len(ids) != 0 {
		t.Errorf("RunTurn calls = %v, want none after shutdown", ids)
	}
}
