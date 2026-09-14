// Package wsclient implements the agent side of the tenzing control-plane
// protocol (docs/adrs/2026-09-12-control-plane-fleet/PROTOCOL.md, protocol
// version "1"). A Client dials the control plane's WebSocket server,
// registers with hello, streams harness events upstream, executes commands
// (query/steer/cancel/approve/shutdown/...) from the plane, and cancels the
// in-flight turn when the connection drops — the turn is only meaningful while the
// control plane can observe it.
package wsclient

import "encoding/json"

// ProtocolVersion is the protocol major version this agent speaks. Bump per
// PROTOCOL.md §6 (breaking changes only).
const ProtocolVersion = "1"

// Subprotocol is the WebSocket subprotocol the agent requests on the upgrade.
const Subprotocol = "tenzing.v1"

// Capabilities advertises what this agent supports, per PROTOCOL.md §1.
type Capabilities struct {
	// Vision is true when the main model accepts image input.
	Vision bool `json:"vision"`
	// Approvals is true when the agent has a live approval path (not
	// --dangerously-skip-permissions / --read-only).
	Approvals bool `json:"approvals"`
}

// LastTurn reports the outcome of a turn that was in flight at the previous
// disconnect, per PROTOCOL.md §1. Absent on a first connect.
type LastTurn struct {
	// ID is the turn's id (the correlation id of its query).
	ID string `json:"id"`
	// Outcome is "cancelled_disconnect" — the only outcome reachable here,
	// since a completed or errored turn ended before the disconnect.
	Outcome string `json:"outcome"`
}

// Hello is the agent's first message after connecting.
type Hello struct {
	Type         string       `json:"type"` // "hello"
	Protocol     string       `json:"protocol"`
	PID          int          `json:"pid"`
	CWD          string       `json:"cwd"`
	Model        string       `json:"model"`
	Capabilities Capabilities `json:"capabilities"`
	// LastTurn is nil on a first connect.
	LastTurn *LastTurn `json:"last_turn,omitempty"`
	// ConversationID is the harness's active conversation — the id the plane
	// passes to --resume to relaunch this agent. Empty when session
	// persistence is disabled.
	ConversationID string `json:"conversation_id,omitempty"`
}

// Welcome is the control plane's registration reply.
type Welcome struct {
	Type    string `json:"type"` // "welcome"
	AgentID string `json:"agent_id"`
}

// Image is one base64-encoded image attachment on a query command.
type Image struct {
	// MediaType is an image/* MIME type.
	MediaType string `json:"media_type"`
	// Data is the base64-encoded image bytes.
	Data string `json:"data"`
}

// Query starts a turn.
type Query struct {
	Type   string  `json:"type"` // "query"
	ID     string  `json:"id"`
	Query  string  `json:"query"`
	Images []Image `json:"images,omitempty"`
}

// Steer injects mid-turn input.
type Steer struct {
	Type    string `json:"type"` // "steer"
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Cancel stops the running turn and drops queued ones.
type Cancel struct {
	Type string `json:"type"` // "cancel"
	ID   string `json:"id"`
}

// Approve answers a pending approval request.
type Approve struct {
	Type     string `json:"type"` // "approve"
	ID       string `json:"id"`
	CallID   string `json:"call_id"`
	Approved bool   `json:"approved"`
	// Glob optionally adds an allow rule when Approved ("allow always").
	// Whether it persists is the wiring's policy (connect mode keeps it in
	// memory unless configured otherwise).
	Glob string `json:"glob,omitempty"`
	// Scope is "session" to keep Glob in memory for this process only, or
	// "always" (the default) to leave persistence to the wiring's policy.
	Scope string `json:"scope,omitempty"`
}

// SetModel switches the main model.
type SetModel struct {
	Type  string `json:"type"` // "set-model"
	ID    string `json:"id"`
	Model string `json:"model"`
}

// SetThinking toggles model reasoning.
type SetThinking struct {
	Type    string `json:"type"` // "set-thinking"
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// Shutdown asks the agent to cancel the running turn (and queued ones), ack,
// close the connection, and exit 0. No reconnect follows.
type Shutdown struct {
	Type string `json:"type"` // "shutdown"
	ID   string `json:"id"`
}

// ShutdownAck is the agent's last message: the turn (if any) has reported
// its cancelled result and the process is exiting.
type ShutdownAck struct {
	Type string `json:"type"` // "shutdown_ack"
	ID   string `json:"id"`
}

// Event carries one harness event envelope upstream.
type Event struct {
	Type string `json:"type"` // "event"
	// ID is the correlation id of the query that started this turn.
	ID string `json:"id"`
	// Envelope is the versioned wire envelope verbatim.
	Envelope json.RawMessage `json:"envelope"`
}

// ApprovalRequest asks the plane to decide a mutating tool call.
type ApprovalRequest struct {
	Type string `json:"type"` // "approval_request"
	// TurnID is the correlation id of the query that started the turn.
	TurnID string `json:"turn_id"`
	// ID is the approval call id the plane echoes back on approve.
	ID    string `json:"id"`
	Tool  string `json:"tool"`
	Input string `json:"input"`
}

// Result reports the end of a turn. Exactly one per accepted query. `id` is
// the correlation id of the query that started the turn.
type Result struct {
	Type string `json:"type"` // "result"
	ID   string `json:"id"`
	// Outcome: "completed" | "error" | "cancelled" | "cancelled_disconnect".
	Outcome string `json:"outcome"`
	// Answer is set on completed.
	Answer string `json:"answer,omitempty"`
	// Error is set on error.
	Error string `json:"error,omitempty"`
	// DeniedTools counts permission-denied tool calls during the turn.
	DeniedTools int `json:"denied_tools"`
	// FilesTouched lists the paths of successful Read/Edit/Write calls during
	// the turn (main agent and subagents), deduplicated, first-seen order.
	FilesTouched []string `json:"files_touched,omitempty"`
}

// Error is a command-level error reply (e.g. bad set-model ref).
type Error struct {
	Type   string `json:"type"` // "error"
	ID     string `json:"id,omitempty"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// decodeIncoming decodes one plane→agent message by its type field. It
// returns a typed pointer or an error for unknown/invalid messages.
func decode(raw []byte) (any, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case "query":
		var m Query
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "steer":
		var m Steer
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "cancel":
		var m Cancel
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "approve":
		var m Approve
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "set-model":
		var m SetModel
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "set-thinking":
		var m SetThinking
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case "shutdown":
		var m Shutdown
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		return nil, &UnknownMessageError{Type: probe.Type}
	}
}

// UnknownMessageError reports a plane→agent message the agent does not
// recognize. Per PROTOCOL.md §6 (additive versioning), unknown types are
// logged and dropped, never fatal.
type UnknownMessageError struct {
	Type string
}

func (e *UnknownMessageError) Error() string { return "unknown message type " + e.Type }
