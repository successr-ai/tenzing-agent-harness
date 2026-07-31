package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

var visionModel = common.ModelDefinition{Name: "vision-model", Provider: "ollama", SupportsVision: true}

func testImages() []common.ImageSource {
	return []common.ImageSource{{MediaType: "image/png", Data: "aGVsbG8="}}
}

func TestRunTurnWithImagesVisionCheck(t *testing.T) {
	scripted := newScriptedAgent(finalStep("ok"))
	h := newTestHarness(t, WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }))
	t.Cleanup(h.Shutdown)

	if h.SupportsVision() {
		t.Error("SupportsVision() = true for non-vision model")
	}
	_, err := h.RunTurnWithImages(context.Background(), "what is this?", testImages())
	if !errors.Is(err, ErrVisionUnsupported) {
		t.Fatalf("err = %v, want ErrVisionUnsupported", err)
	}
	assertCallCount(t, scripted, 0) // rejected before any LLM/agent work
}

func TestRunTurnWithImagesRunsAndEmits(t *testing.T) {
	redirectHome(t)
	scripted := newScriptedAgent(finalStep("a cat"))
	bus := eventbus.NewEventBus()
	ch := bus.Subscribe(64)
	h, err := New(&stubLLM{model: visionModel},
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithSystemPrompt("test"),
		WithEventBus(bus),
		WithContextFilesDisabled(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(h.Shutdown)

	if !h.SupportsVision() {
		t.Error("SupportsVision() = false for vision model")
	}
	answer, err := h.RunTurnWithImages(context.Background(), "what is this?", testImages())
	if err != nil {
		t.Fatalf("RunTurnWithImages: %v", err)
	}
	assertAnswerContains(t, answer, "a cat")

	var attached *core.ImagesAttachedEvent
	for done := false; !done; {
		select {
		case ev := <-ch:
			if ia, ok := ev.(core.ImagesAttachedEvent); ok {
				attached = &ia
				done = true
			}
		default:
			done = true
		}
	}
	if attached == nil {
		t.Fatal("no ImagesAttachedEvent emitted")
	}
	if len(attached.Images) != 1 || attached.Images[0].MediaType != "image/png" {
		t.Errorf("event images = %#v, want the test image", attached.Images)
	}
}

// switchableAgent is a ScriptedAgent that accepts SetLLM, so harness SetLLM works.
type switchableAgent struct{ *ScriptedAgent }

func (a *switchableAgent) SetLLM(_ common.LLM) {}

// SetLLM updates the capability check.
func TestSetLLMUpdatesVisionCapability(t *testing.T) {
	agent := &switchableAgent{ScriptedAgent: newScriptedAgent(finalStep("ok"))}
	h := newTestHarness(t, WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return agent, nil })) // testModel: no vision
	t.Cleanup(h.Shutdown)

	if h.SupportsVision() {
		t.Fatal("unexpected vision support")
	}
	if err := h.SetLLM(&stubLLM{model: visionModel}); err != nil {
		t.Fatalf("SetLLM: %v", err)
	}
	if !h.SupportsVision() {
		t.Error("SupportsVision() = false after switching to vision model")
	}
}
