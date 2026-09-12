// Package approvals tracks tool calls waiting for a human decision.
package approvals

import "sync"

// Pending is one unanswered AskUser request.
type Pending struct {
	// Respond answers the call: true runs the tool, false denies it. It is
	// idempotent, so a late answer to a timed-out request is a harmless no-op.
	Respond func(approved bool)
	// Tool is the tool name as the harness registered it (case preserved).
	Tool string
	// Input is the call's raw JSON input, kept so a decision can be previewed
	// (Edit/Write diff) or a bash allow glob suggested.
	Input string
}

// Registry holds pending requests keyed by tool-call ID. Safe for
// concurrent use. Entries leave when answered (Take/Remove); an entry whose
// request timed out on the harness side is simply never taken.
type Registry struct {
	mu      sync.Mutex
	pending map[string]Pending
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{pending: make(map[string]Pending)}
}

// Add records p under callID, replacing any earlier entry for the same ID.
func (r *Registry) Add(callID string, p Pending) {
	r.mu.Lock()
	r.pending[callID] = p
	r.mu.Unlock()
}

// Get returns the entry for callID without removing it, for read-only
// operations such as previews.
func (r *Registry) Get(callID string) (Pending, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[callID]
	return p, ok
}

// Take removes and returns the entry for callID, for answering it.
func (r *Registry) Take(callID string) (Pending, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[callID]
	if ok {
		delete(r.pending, callID)
	}
	return p, ok
}

// Remove drops the entry for callID if present.
func (r *Registry) Remove(callID string) {
	r.mu.Lock()
	delete(r.pending, callID)
	r.mu.Unlock()
}

// Len reports how many requests are pending.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}
