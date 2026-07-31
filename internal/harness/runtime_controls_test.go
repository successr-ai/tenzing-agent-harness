package harness

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// namedStubLLM reports the model name it was built for, so SetLLM is
// observable through GetCurrentModel.
type namedStubLLM struct {
	stubLLM
	name string
}

func (s *namedStubLLM) GetCurrentModel() string { return s.name }
func (s *namedStubLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: s.name, Provider: "ollama"}
}

// newControlHarness builds a harness on the DEFAULT brain (real agent) with
// stub LLMs, so the SetLLM/SetThinking sub-interfaces exist.
func newControlHarness(t *testing.T) (*Harness, <-chan core.Event) {
	t.Helper()
	redirectHome(t)
	bus := eventbus.NewEventBus()
	h, err := New(&namedStubLLM{name: "stub-model"},
		WithSystemPrompt("test"),
		WithEventBus(bus),
		WithContextFilesDisabled(),
		WithSubagentDepth(0),
		WithPermissionsDisabled(),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(h.Shutdown)
	return h, bus.Subscribe(16)
}

func TestSetLLMSwitchesBetweenTurns(t *testing.T) {
	h, ch := newControlHarness(t)

	if got := h.GetCurrentModel(); got != "stub-model" {
		t.Fatalf("initial model = %q", got)
	}
	other := &namedStubLLM{name: "other-model"}
	if err := h.SetLLM(other); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}
	if got := h.GetCurrentModel(); got != "other-model" {
		t.Errorf("model after switch = %q, want other-model", got)
	}
	if got := h.CurrentModel().GetName(); got != "other-model" {
		t.Errorf("CurrentModel() = %q, want other-model", got)
	}

	ev := <-ch
	mc, ok := ev.(core.ModelChangedEvent)
	if !ok {
		t.Fatalf("event = %T, want ModelChangedEvent", ev)
	}
	if mc.From != "stub-model" || mc.To != "other-model" {
		t.Errorf("event = %+v", mc)
	}
}

func TestSetThinkingEmitsEvent(t *testing.T) {
	h, ch := newControlHarness(t)
	if err := h.SetThinking(true); err != nil {
		t.Fatalf("SetThinking: %v", err)
	}
	ev := <-ch
	tc, ok := ev.(core.ThinkingChangedEvent)
	if !ok || !tc.Enabled {
		t.Fatalf("event = %#v, want ThinkingChangedEvent{Enabled:true}", ev)
	}
}

func TestCompactEmptyHistoryIsNoop(t *testing.T) {
	h, ch := newControlHarness(t)
	if err := h.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact on empty history: %v", err)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %T for no-op compact", ev)
	default:
	}
}

// A custom brain without the sub-interfaces gets clean errors, not panics.
// (Compact works regardless — the context store, not the brain, owns it.)
func TestControlsUnsupportedByCustomBrain(t *testing.T) {
	h := newTestHarness(t) // stubAgent brain: no SetLLM/SetThinking
	if err := h.SetLLM(&stubLLM{}); err == nil {
		t.Error("SetLLM on stub brain should error")
	}
	if err := h.SetThinking(true); err == nil {
		t.Error("SetThinking on stub brain should error")
	}
}
