// Package session persists conversations as append-only JSONL files so a
// process restart can resume with full history (not just the compression
// summary). One file per conversation: a header line followed by one entry
// per line. Persistence is best-effort — failures are logged, never
// propagated into the agent loop.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
)

// Version is the session file format version (header "version" field).
const Version = 1

// Entry types. Each entry line carries exactly one of these in "type".
const (
	// TypeHeader is the first line of every session file.
	TypeHeader = "session"
	// TypeUser is a user query starting a turn.
	TypeUser = "user"
	// TypeAssistant is assistant text from an LLM response (empty-text
	// responses, i.e. pure tool calls, are not persisted — the call input
	// lives on its tool_result entry).
	TypeAssistant = "assistant"
	// TypeToolResult records one tool call and its output.
	TypeToolResult = "tool_result"
	// TypeSteering is a user message injected mid-turn.
	TypeSteering = "steering"
	// TypeCompaction records a context compression; replay restarts from
	// its summary plus the retained tail.
	TypeCompaction = "compaction"
	// TypeTodo snapshots the todo plan at the end of a turn.
	TypeTodo = "todo"
	// TypeLabel names the session (rename support). Out-of-band metadata:
	// parent_id is empty and history reconstruction skips it; the last
	// label entry in the file wins.
	TypeLabel = "label"
	// TypeModelChange records a mid-session model switch (Model = new
	// model, Text = previous). Informational: replay skips it and resume
	// does not re-apply it — the model comes from config.
	TypeModelChange = "model_change"
	// TypeThinking records a reasoning toggle (Text "on"/"off"). The last
	// one wins on resume.
	TypeThinking = "thinking"
	// TypeImage records one image attached to the following user turn. The
	// bytes live in a sidecar blob file (<session dir>/blobs/<sha256>); the
	// entry stores only media type + hash so JSONL lines stay small. Replay
	// reconstructs an image-block user message, or a text placeholder when
	// the blob is gone.
	TypeImage = "image"
)

// Header is the first JSONL line of a session file.
type Header struct {
	Type           string    `json:"type"` // always TypeHeader
	Version        int       `json:"version"`
	ConversationID string    `json:"conversation_id"`
	Cwd            string    `json:"cwd"`
	Model          string    `json:"model"`
	Time           time.Time `json:"time"`
}

// Entry is one JSONL line after the header. Fields beyond the common four
// are populated per Type (see the Type* constants).
type Entry struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id"`
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`

	// TypeUser / TypeAssistant / TypeSteering
	Text string `json:"text,omitempty"`

	// TypeAssistant
	Model string `json:"model,omitempty"`

	// TypeToolResult
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`

	// TypeCompaction
	Summary string `json:"summary,omitempty"`

	// TypeTodo
	Tasks []todo.Task `json:"tasks,omitempty"`

	// TypeImage
	MediaType string `json:"media_type,omitempty"`
	Blob      string `json:"blob,omitempty"` // sha256 hex of the raw image bytes
}

// newEntryID returns a fresh 8-hex-char entry ID (same shape as runner IDs).
func newEntryID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
