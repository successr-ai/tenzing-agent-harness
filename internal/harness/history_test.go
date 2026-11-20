package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func TestValidateHistory(t *testing.T) {
	user := common.NewUserMessage
	asst := common.NewAssistantMessage
	callA := common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewToolUseContent("a", "read", json.RawMessage(`{}`)),
	}}
	resultA := common.Message{Role: common.RoleTool, Content: []common.ContentBlock{
		common.NewToolResultContent("a", "read", "ok"),
	}}
	resultB := common.Message{Role: common.RoleTool, Content: []common.ContentBlock{
		common.NewToolResultContent("b", "read", "ok"),
	}}

	tests := []struct {
		name    string
		msgs    []common.Message
		wantErr string
	}{
		{"empty", nil, ""},
		{"text turns", []common.Message{user("q1"), asst("a1"), user("q2"), asst("a2")}, ""},
		{"tool round trip", []common.Message{user("q"), callA, resultA, asst("done")}, ""},
		{"system message", []common.Message{common.NewSystemMessage("s"), asst("a")}, "not allowed"},
		{"assistant first", []common.Message{asst("a")}, "assistant where user expected"},
		{"two users", []common.Message{user("q1"), user("q2"), asst("a")}, "where assistant expected"},
		{"ends on user", []common.Message{user("q1"), asst("a1"), user("q2")}, "last message"},
		{"empty content", []common.Message{{Role: common.RoleUser}, asst("a")}, "empty content"},
		{"unanswered tool call", []common.Message{user("q"), callA, user("q2"), asst("a")}, "no results"},
		{"dangling tool call", []common.Message{user("q"), callA}, "no results"},
		{"wrong tool result", []common.Message{user("q"), callA, resultB, asst("a")}, "answers no pending"},
		{"tool result without call", []common.Message{user("q"), asst("a"), resultA, asst("b")}, "answers no pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHistory(tt.msgs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateHistory: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateHistory = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestSetHistoryFeedsNextTurn proves an injected transcript reaches the
// model as real messages ahead of the new question.
func TestSetHistoryFeedsNextTurn(t *testing.T) {
	h := newTestHarness(t, WithSessionDisabled())
	ctx := context.Background()

	err := h.SetHistory([]common.Message{
		common.NewUserMessage("earlier question"),
		common.NewAssistantMessage("earlier answer"),
	})
	if err != nil {
		t.Fatalf("SetHistory: %v", err)
	}
	if _, err := h.RunTurn(ctx, "new question"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	msgs, err := h.mainStore.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) < 3 {
		t.Fatalf("history has %d messages, want injected turns plus the new one", len(msgs))
	}
	if got := msgs[0].Content[0].Text; got != "earlier question" {
		t.Errorf("msgs[0] = %q, want the injected question", got)
	}
	if got := msgs[2].Content[0].Text; got != "new question" {
		t.Errorf("msgs[2] = %q, want the new question", got)
	}
}

// TestSetHistoryRejectsInvalidAndKeepsLive proves a bad transcript fails
// without touching the current conversation.
func TestSetHistoryRejectsInvalidAndKeepsLive(t *testing.T) {
	h := newTestHarness(t, WithSessionDisabled())
	ctx := context.Background()
	if err := h.mainStore.AppendUser(ctx, "keep me"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}

	err := h.SetHistory([]common.Message{common.NewAssistantMessage("no question")})
	if err == nil || !strings.Contains(err.Error(), "invalid history") {
		t.Fatalf("SetHistory = %v, want invalid history error", err)
	}
	if msgs, _ := h.mainStore.Messages(ctx); len(msgs) != 1 {
		t.Errorf("history has %d messages after rejected SetHistory, want 1", len(msgs))
	}
}
