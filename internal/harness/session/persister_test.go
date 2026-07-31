package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
)

func TestPersisterAppendsMainEventsOnly(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, dir, "conv1")
	bus := eventbus.NewEventBus()
	defer bus.Close()

	stop := StartPersister(bus, s, "main-id", func() []todo.Task {
		return []todo.Task{{ID: "t1", Description: "snapshotted", Status: "pending"}}
	})
	defer stop()

	bus.Emit(core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "main-id"), Query: "the-query"})
	bus.Emit(core.LLMResponseEvent{BaseEvent: core.NewBaseEvent(core.EventLLMResponse, "main-id"), Text: "the-answer", Model: "m"})
	bus.Emit(core.LLMResponseEvent{BaseEvent: core.NewBaseEvent(core.EventLLMResponse, "main-id"), Text: ""}) // pure tool call: skipped
	bus.Emit(core.ToolSucceededEvent{BaseEvent: core.NewBaseEvent(core.EventToolSucceeded, "main-id"), ToolName: "Read", Input: "{}", Output: "ok-output"})
	bus.Emit(core.ToolFailedEvent{BaseEvent: core.NewBaseEvent(core.EventToolFailed, "main-id"), ToolName: "bash", Input: "{}", Error: "boom"})
	bus.Emit(core.SteeringInjectedEvent{BaseEvent: core.NewBaseEvent(core.EventSteeringInjected, "main-id"), Message: "steer-msg"})
	bus.Emit(core.ContextCompressedEvent{BaseEvent: core.NewBaseEvent(core.EventContextCompressed, "main-id"), Summary: "the-summary"})
	bus.Emit(core.ModelChangedEvent{BaseEvent: core.NewBaseEvent(core.EventModelChanged, "main-id"), From: "old-model", To: "new-model"})
	bus.Emit(core.ThinkingChangedEvent{BaseEvent: core.NewBaseEvent(core.EventThinkingChanged, "main-id"), Enabled: true})
	// subagent events must be filtered out
	bus.Emit(core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "main-id_child"), Query: "SUBAGENT-QUERY"})
	bus.Emit(core.ToolSucceededEvent{BaseEvent: core.NewBaseEvent(core.EventToolSucceeded, "main-id_child"), ToolName: "Read", Output: "SUBAGENT-OUTPUT"})
	// turn end triggers the todo snapshot
	bus.Emit(core.TurnCompletedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnCompleted, "main-id"), FinalAnswer: "the-answer"})

	// dispatch is async and the file is created lazily; poll until the
	// last expected entry lands.
	deadline := time.Now().Add(2 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(s.Path()); err == nil {
			content = string(data)
			if strings.Contains(content, "snapshotted") {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != 10 { // header + user, assistant, 2 tool results, steering, compaction, model_change, thinking, todo
		t.Fatalf("lines = %d, want 10:\n%s", len(lines), content)
	}
	for _, want := range []string{"the-query", "the-answer", "ok-output", "boom", "steer-msg", "the-summary", "new-model", `"thinking"`, "snapshotted"} {
		if !strings.Contains(content, want) {
			t.Errorf("session file missing %q", want)
		}
	}
	if strings.Contains(content, "SUBAGENT") {
		t.Error("subagent events leaked into the session file")
	}
}
