package reminders

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

func TestCollectsNonEmptyReminders(t *testing.T) {
	ext := New(
		func() string { return "todo: 2 open" },
		func() string { return "" }, // empty providers are skipped
	)
	tc := &core.TurnContext{}
	if err := ext.BeforeIteration(context.Background(), tc); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(tc.Reminders) != 1 || tc.Reminders[0] != "todo: 2 open" {
		t.Fatalf("got %v", tc.Reminders)
	}
}
