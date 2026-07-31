package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// steerTools steers the loop from inside the first tool execution, so the
// message is guaranteed to be queued before the tool boundary.
type steerTools struct {
	fakeTools
	loop *Loop
	msg  string
	once sync.Once
}

func (s *steerTools) Execute(ctx context.Context, call ToolCall) ToolResult {
	s.once.Do(func() {
		if err := s.loop.Steer(s.msg); err != nil {
			panic(err)
		}
	})
	return s.fakeTools.Execute(ctx, call)
}

func TestSteerInjectsMessageAtToolBoundary(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "1", Name: "a", Input: `{}`}),
		{FinalAnswer: "done"},
	}}
	tools := &steerTools{msg: "change course"}
	fctx := newFakeContext()
	emitter := &fakeEmitter{}

	l := newTestLoop(t, model, tools, fctx, func(cfg *LoopConfig) { cfg.Emitter = emitter })
	tools.loop = l
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	// context call order: steering user message lands after the tool results
	want := []string{"AppendUser", "Messages", "AppendAssistant", "AppendToolResults", "AppendUser", "Messages", "AppendAssistant"}
	got := fctx.calls()
	if len(got) != len(want) {
		t.Fatalf("context calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("context call[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	found := false
	for _, ev := range emitter.eventTypes() {
		if ev == EventSteeringInjected {
			found = true
		}
	}
	if !found {
		t.Error("no SteeringInjectedEvent emitted")
	}
}

func TestSteerBufferFull(t *testing.T) {
	l := newTestLoop(t, &fakeModel{}, newFakeTools(nil), newFakeContext())
	for i := 0; i < steeringBufferSize; i++ {
		if err := l.Steer(fmt.Sprintf("msg %d", i)); err != nil {
			t.Fatalf("Steer(%d) = %v, want nil", i, err)
		}
	}
	if err := l.Steer("overflow"); err == nil {
		t.Error("Steer on full buffer = nil, want error")
	}
}

func TestLoopStateReporting(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{{FinalAnswer: "done"}}}
	l := newTestLoop(t, model, newFakeTools(nil), newFakeContext())
	if got := l.State(); got != string(LoopStateStarted) {
		t.Errorf("initial State() = %q, want %q", got, LoopStateStarted)
	}
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := l.State(); got != string(LoopStateStopped) {
		t.Errorf("State() after turn = %q, want %q", got, LoopStateStopped)
	}
}

func TestMutatingToolIsBarrier(t *testing.T) {
	// a (read-only) and b (mutating) and c (read-only): b must start only
	// after a completes, and c only after b completes.
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "1", Name: "a", Input: `{}`},
			ToolCall{ID: "2", Name: "b", Input: `{}`},
			ToolCall{ID: "3", Name: "c", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	tools := newSleepyTools(map[string]time.Duration{
		"a": 40 * time.Millisecond,
		"b": 40 * time.Millisecond,
		"c": 5 * time.Millisecond,
	})
	tools.readOnly = map[string]bool{"a": true, "b": false, "c": true}

	l := newTestLoop(t, model, tools, newFakeContext())
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	aDone, bDone, cDone := tools.completedAt("a"), tools.completedAt("b"), tools.completedAt("c")
	if bDone.Before(aDone) {
		t.Errorf("mutating b completed at %v before read-only a at %v — barrier violated", bDone, aDone)
	}
	if cDone.Before(bDone) {
		t.Errorf("c completed at %v before mutating barrier b at %v", cDone, bDone)
	}
}

func TestReadOnlyRunConcurrentMutatingAlone(t *testing.T) {
	// two read-only tools sleep 60ms each; concurrent → segment ≈ 60ms.
	// Serial would be 120ms+. Then one mutating tool runs alone.
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(
			ToolCall{ID: "1", Name: "r1", Input: `{}`},
			ToolCall{ID: "2", Name: "r2", Input: `{}`},
			ToolCall{ID: "3", Name: "w", Input: `{}`},
		),
		{FinalAnswer: "done"},
	}}
	tools := newSleepyTools(map[string]time.Duration{
		"r1": 60 * time.Millisecond,
		"r2": 60 * time.Millisecond,
		"w":  time.Millisecond,
	})
	tools.readOnly = map[string]bool{"r1": true, "r2": true, "w": false}

	l := newTestLoop(t, model, tools, newFakeContext())
	start := time.Now()
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	wall := time.Since(start)
	// serial read-only segment would be >= 120ms
	if wall >= 110*time.Millisecond {
		t.Errorf("wall = %v, want < 110ms (read-only segment concurrent)", wall)
	}
	wDone := tools.completedAt("w")
	for _, name := range []string{"r1", "r2"} {
		if wDone.Before(tools.completedAt(name)) {
			t.Errorf("mutating w completed before read-only %s — barrier violated", name)
		}
	}
}

func TestRunTurnWithImagesAppendsBlocks(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{{FinalAnswer: "seen"}}}
	fctx := newFakeContext()
	l := newTestLoop(t, model, newFakeTools(nil), fctx)

	images := []common.ImageSource{{MediaType: "image/png", Data: "aGVsbG8="}}
	if _, err := l.RunTurnWithImages(context.Background(), "what is this?", images); err != nil {
		t.Fatal(err)
	}

	fctx.mu.Lock()
	defer fctx.mu.Unlock()
	if len(fctx.msgs) == 0 {
		t.Fatal("no messages appended")
	}
	first := fctx.msgs[0]
	if first.Role != common.RoleUser || len(first.Content) != 2 {
		t.Fatalf("first message = %+v, want user message with text + image blocks", first)
	}
	if first.Content[1].Image == nil || first.Content[1].Image.MediaType != "image/png" {
		t.Errorf("second block = %+v, want image block", first.Content[1])
	}
	if fctx.callLog[0] != "AppendUserContent" {
		t.Errorf("first context call = %q, want AppendUserContent", fctx.callLog[0])
	}
}

// Guard: steering messages queued while idle survive to the next turn's
// first tool boundary.
func TestSteerWhileIdleInjectsNextTurn(t *testing.T) {
	model := &fakeModel{steps: []ReasoningResult{
		toolCallResult(ToolCall{ID: "1", Name: "a", Input: `{}`}),
		{FinalAnswer: "done"},
	}}
	fctx := newFakeContext()
	l := newTestLoop(t, model, newFakeTools(nil), fctx)
	if err := l.Steer("queued while idle"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	fctx.mu.Lock()
	var steered bool
	for _, m := range fctx.msgs {
		if m.Role == common.RoleUser && common.CombinedText(m.Content) == "queued while idle" {
			steered = true
		}
	}
	fctx.mu.Unlock()
	if !steered {
		t.Error("idle-queued steering message never injected")
	}
}
