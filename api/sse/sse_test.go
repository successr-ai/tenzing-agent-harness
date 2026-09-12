package sse

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeRecorder is a goroutine-safe ResponseWriter+Flusher: the stream
// goroutine writes while the test reads.
type safeRecorder struct {
	mu   sync.Mutex
	hdr  http.Header
	code int
	buf  bytes.Buffer
}

func newSafeRecorder() *safeRecorder { return &safeRecorder{hdr: http.Header{}, code: http.StatusOK} }

func (r *safeRecorder) Header() http.Header { return r.hdr }
func (r *safeRecorder) WriteHeader(code int) {
	r.mu.Lock()
	r.code = code
	r.mu.Unlock()
}
func (r *safeRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}
func (r *safeRecorder) Flush() {}
func (r *safeRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// stream connects one more client to b (bringing the total to want) and
// returns the recorder plus a function that ends the request and waits for
// ServeHTTP to return.
func stream(t *testing.T, b *Broadcaster, want int) (*safeRecorder, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rec := newSafeRecorder()
	done := make(chan struct{})
	go func() {
		b.ServeHTTP(rec, httptest.NewRequest("GET", "/events", nil).WithContext(ctx))
		close(done)
	}()
	waitFor(t, "client to subscribe", func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.clients) == want
	})
	return rec, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("ServeHTTP did not return after the request ended")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPublishNoClients(t *testing.T) {
	b := NewBroadcaster()
	b.Publish("x", map[string]int{"a": 1}) // must not block or panic
	b.PublishRaw("", "")
}

func TestServeHTTPWritesFramesAndEndsOnRequestCancel(t *testing.T) {
	b := NewBroadcaster()
	rec, end := stream(t, b, 1)

	b.Publish("status", map[string]string{"state": "idle"})
	b.PublishRaw("text_delta", "line1\nline2")
	waitFor(t, "frames", func() bool { return strings.Count(rec.String(), "\n\n") == 2 })
	end()

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	want := "event: status\ndata: {\"state\":\"idle\"}\n\nevent: text_delta\ndata: line1\ndata: line2\n\n"
	if got := rec.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestTwoClientsBothReceive(t *testing.T) {
	b := NewBroadcaster()
	r1, end1 := stream(t, b, 1)
	r2, end2 := stream(t, b, 2)
	b.PublishRaw("e", "d")
	waitFor(t, "both clients", func() bool {
		return strings.Contains(r1.String(), "data: d") && strings.Contains(r2.String(), "data: d")
	})
	end1()
	end2()
}

func TestSlowClientDropsMessages(t *testing.T) {
	b := NewBroadcaster()
	ch := b.subscribe() // a subscriber nobody drains
	defer b.unsubscribe(ch)
	for range clientBuffer + 10 {
		b.PublishRaw("e", "d") // must never block
	}
	if len(ch) != clientBuffer {
		t.Errorf("buffered = %d, want %d", len(ch), clientBuffer)
	}
}

func TestCloseEndsStreamsAndIsIdempotent(t *testing.T) {
	b := NewBroadcaster()
	rec := newSafeRecorder()
	done := make(chan struct{})
	go func() {
		b.ServeHTTP(rec, httptest.NewRequest("GET", "/events", nil))
		close(done)
	}()
	waitFor(t, "subscribe", func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.clients) == 1
	})
	b.Close()
	b.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after Close")
	}
	b.Publish("after", nil) // no-op, must not panic
}

// noFlush is a ResponseWriter without http.Flusher.
type noFlush struct{ http.ResponseWriter }

func TestServeHTTPRequiresFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	NewBroadcaster().ServeHTTP(noFlush{rec}, httptest.NewRequest("GET", "/events", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestPublishUnmarshalableValueIsDropped(t *testing.T) {
	b := NewBroadcaster()
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	b.Publish("bad", make(chan int)) // json.Marshal fails
	if len(ch) != 0 {
		t.Error("unmarshalable payload was published")
	}
}
