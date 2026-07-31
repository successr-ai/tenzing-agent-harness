package harness

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
)

// A completed turn must land in the session file: header, user entry, and
// the assistant's final answer. Persistence is async (event bus), so poll.
func TestSessionPersistsTurn(t *testing.T) {
	dir := t.TempDir()
	h := newTestHarness(t, WithSessionDir(dir), WithConversationID("sess-persist"))
	defer h.Shutdown()

	if _, err := h.RunTurn(context.Background(), "remember the magic word: xyzzy"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(h.sessionStore.Path()); err == nil {
			content = string(data)
			if strings.Contains(content, "xyzzy") && strings.Contains(content, `"type":"assistant"`) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(content, `"type":"session"`) {
		t.Errorf("session file missing header:\n%s", content)
	}
	if !strings.Contains(content, "xyzzy") {
		t.Errorf("session file missing user entry:\n%s", content)
	}
	if !strings.Contains(content, `"type":"assistant"`) {
		t.Errorf("session file missing assistant entry:\n%s", content)
	}
}

// Resuming a conversation restores the persisted todo plan into the
// harness's todo store.
func TestSessionResumeRestoresTodos(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	s := session.NewStore(dir, cwd, "resume-1", "m", time.Now)
	s.Append(session.Entry{Type: session.TypeUser, Text: "plan the work"})
	s.Append(session.Entry{Type: session.TypeTodo, Tasks: []todo.Task{
		{ID: "abc12345", Description: "ship the feature", Status: "pending"},
	}})
	s.Close()

	h := newTestHarness(t, WithSessionDir(dir), WithConversationID("resume-1"))
	defer h.Shutdown()

	reminder := h.todoFile.FormatReminder()
	if !strings.Contains(reminder, "ship the feature") {
		t.Errorf("todo plan not restored from session, reminder:\n%s", reminder)
	}
}

// WithSessionDisabled must leave no store and write nothing.
func TestSessionDisabled(t *testing.T) {
	h := newTestHarness(t, WithSessionDisabled())
	defer h.Shutdown()
	if h.sessionStore != nil {
		t.Error("sessionStore should be nil when disabled")
	}
}
