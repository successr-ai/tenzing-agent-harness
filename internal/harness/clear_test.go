package harness

import (
	"context"
	"strings"
	"testing"
)

// TestClearResetsHistoryAndRotatesConversation proves /clear's contract:
// the model's context is emptied and the harness continues under a fresh
// conversation ID, so the cleared transcript stays on disk under the old
// one.
func TestClearResetsHistoryAndRotatesConversation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	if err := h.mainStore.AppendUser(ctx, "remember this"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	before := h.ConversationID()

	if err := h.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	msgs, err := h.mainStore.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("history has %d messages after Clear, want 0", len(msgs))
	}
	after := h.ConversationID()
	if after == before {
		t.Errorf("conversation ID %q unchanged after Clear", after)
	}
	if after == "" {
		t.Error("conversation ID is empty after Clear")
	}
}

// TestClearWithSessionsDisabled proves Clear still works — and still moves
// the conversation ID — when nothing is being persisted.
func TestClearWithSessionsDisabled(t *testing.T) {
	h := newTestHarness(t, WithSessionDisabled())
	before := h.ConversationID()
	if err := h.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if h.ConversationID() == before {
		t.Error("conversation ID unchanged with persistence disabled")
	}
}

// TestResumeRejectsUnknownConversation proves a bad ID fails loudly and
// leaves the live conversation untouched, rather than silently clearing it.
func TestResumeRejectsUnknownConversation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	if err := h.mainStore.AppendUser(ctx, "keep me"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	before := h.ConversationID()

	err := h.Resume("no-such-conversation")
	if err == nil {
		t.Fatal("Resume(unknown) succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no-such-conversation") {
		t.Errorf("error %q does not name the conversation", err)
	}

	msgs, _ := h.mainStore.Messages(ctx)
	if len(msgs) != 1 {
		t.Errorf("history has %d messages after a failed Resume, want 1", len(msgs))
	}
	if h.ConversationID() != before {
		t.Error("conversation ID moved on a failed Resume")
	}
}

// TestResumeRequiresPersistence proves Resume reports the real reason when
// sessions are disabled instead of failing as "not found".
func TestResumeRequiresPersistence(t *testing.T) {
	h := newTestHarness(t, WithSessionDisabled())
	err := h.Resume("anything")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("Resume with persistence disabled = %v, want a 'disabled' error", err)
	}
}

// TestClearThenResumeRestoresTheClearedConversation is the round trip that
// makes /clear safe: run a turn, clear it away, then resume the old ID and
// get the history back.
func TestClearThenResumeRestoresTheClearedConversation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	if _, err := h.RunTurn(ctx, "the original question"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	original := h.ConversationID()

	if err := h.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if msgs, _ := h.mainStore.Messages(ctx); len(msgs) != 0 {
		t.Fatalf("history has %d messages after Clear, want 0", len(msgs))
	}

	if err := h.Resume(original); err != nil {
		t.Fatalf("Resume(%s): %v", original, err)
	}
	if got := h.ConversationID(); got != original {
		t.Errorf("ConversationID = %q after resume, want %q", got, original)
	}

	msgs, err := h.mainStore.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("resumed history is empty, want the pre-clear turn")
	}
	var text strings.Builder
	for _, m := range msgs {
		for _, b := range m.Content {
			text.WriteString(b.Text)
		}
	}
	if !strings.Contains(text.String(), "the original question") {
		t.Errorf("resumed history does not contain the original turn: %q", text.String())
	}
}
