package contextstore

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// TestClearAndReplace covers the two history-swapping entry points /clear
// and session resume rely on, including the pending tool_use blocks that
// would otherwise leak into the new history and break the next request.
func TestClearAndReplace(t *testing.T) {
	ctx := context.Background()

	t.Run("clear empties the history", func(t *testing.T) {
		s := New(Config{})
		if err := s.AppendUser(ctx, "hello"); err != nil {
			t.Fatalf("AppendUser: %v", err)
		}
		s.Clear()
		msgs, err := s.Messages(ctx)
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(msgs) != 0 {
			t.Errorf("got %d messages after Clear, want 0", len(msgs))
		}
	})

	t.Run("replace swaps the history", func(t *testing.T) {
		s := New(Config{})
		if err := s.AppendUser(ctx, "old"); err != nil {
			t.Fatalf("AppendUser: %v", err)
		}
		s.Replace([]common.Message{common.NewUserMessage("new")})
		msgs, _ := s.Messages(ctx)
		if len(msgs) != 1 || msgs[0].Content[0].Text != "new" {
			t.Errorf("got %+v, want the single replacement message", msgs)
		}
	})

	t.Run("replace drops pending tool_use blocks", func(t *testing.T) {
		s := New(Config{})
		s.pending = []common.ContentBlock{{Type: common.ContentTypeToolUse, ToolUseID: "call-1"}}
		s.Replace([]common.Message{common.NewUserMessage("fresh")})
		if len(s.pending) != 0 {
			t.Errorf("got %d pending blocks after Replace, want 0", len(s.pending))
		}
	})

	t.Run("replace copies its input", func(t *testing.T) {
		s := New(Config{})
		in := []common.Message{common.NewUserMessage("mine")}
		s.Replace(in)
		in[0] = common.NewUserMessage("mutated by the caller")
		msgs, _ := s.Messages(ctx)
		if msgs[0].Content[0].Text != "mine" {
			t.Errorf("store followed a caller-side mutation: %q", msgs[0].Content[0].Text)
		}
	})
}
