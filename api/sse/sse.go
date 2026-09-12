// Package sse fans server-sent events out to every connected HTTP stream.
package sse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// clientBuffer is how many messages a slow client may fall behind before
// further messages to it are dropped.
const clientBuffer = 128

// Broadcaster copies each published message to every connected stream.
// Publishing never blocks: a client whose buffer is full misses the message.
// Close ends every open stream so a graceful HTTP shutdown can drain.
type Broadcaster struct {
	mu      sync.RWMutex
	clients map[chan message]struct{}
	once    sync.Once
	done    chan struct{} // closed by Close; unblocks ServeHTTP
}

// message is one SSE frame before wire formatting.
type message struct {
	event string
	data  string
}

// NewBroadcaster returns a broadcaster with no clients.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		clients: make(map[chan message]struct{}),
		done:    make(chan struct{}),
	}
}

// Publish JSON-encodes v and sends it to every client under event. A value
// that cannot be marshaled is logged and dropped.
func (b *Broadcaster) Publish(event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("sse json marshal", "event", event, "error", err)
		return
	}
	b.PublishRaw(event, string(data))
}

// PublishRaw sends data verbatim to every client under event. Newlines in
// data are preserved on the wire as continuation `data:` lines.
func (b *Broadcaster) PublishRaw(event, data string) {
	msg := message{event: event, data: data}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Close ends every open stream. Idempotent. Publishing after Close is a
// no-op once the streams have drained.
func (b *Broadcaster) Close() {
	b.once.Do(func() { close(b.done) })
}

// ServeHTTP streams events to one client until the request context ends or
// the broadcaster is closed. Conforms to the http.Handler interface.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := b.subscribe()
	defer b.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-b.done:
			// server shutdown: end the stream so http.Server.Shutdown can
			// drain — it waits for handlers but never cancels r.Context().
			return
		case msg := <-ch:
			writeFrame(w, msg)
			flusher.Flush()
		}
	}
}

// subscribe registers a new client channel.
func (b *Broadcaster) subscribe() chan message {
	ch := make(chan message, clientBuffer)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// unsubscribe removes a client channel.
func (b *Broadcaster) unsubscribe(ch chan message) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// writeFrame formats one message as an SSE frame.
func writeFrame(w http.ResponseWriter, msg message) {
	data := strings.ReplaceAll(msg.data, "\n", "\ndata: ")
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.event, data)
}
