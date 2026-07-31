package toolport

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
)

// fakeStaticExt is an extension providing a static tool bundle.
type fakeStaticExt struct {
	name  string
	specs []core.ToolSpec
}

func (f *fakeStaticExt) Name() string           { return f.name }
func (f *fakeStaticExt) Tools() []core.ToolSpec { return f.specs }

// fakeDynamicExt is an extension whose tool set changes between turns.
type fakeDynamicExt struct {
	name string
	mu   sync.Mutex
	cur  []core.ToolSpec
}

func (f *fakeDynamicExt) Name() string { return f.name }
func (f *fakeDynamicExt) CurrentTools(ctx context.Context) []core.ToolSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}
func (f *fakeDynamicExt) set(specs []core.ToolSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cur = specs
}

func spec(name, origin, output string) core.ToolSpec {
	return core.ToolSpec{
		Definition: common.ToolDefinition{Name: name, Description: name + " tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Origin:     origin,
		Execute: func(ctx context.Context, call core.ToolCall) core.ToolResult {
			return core.ToolResult{ToolUseID: call.ID, Output: output}
		},
	}
}

func newTestComposite(t *testing.T, exts ...core.Extension) *Composite {
	t.Helper()
	c, err := NewComposite(newTestRegistry(t), core.NewExtensions(exts...))
	if err != nil {
		t.Fatalf("NewComposite: %v", err)
	}
	return c
}

// newTestRegistry returns a Registry seeded with the standard builtin tools,
// for tests that assert on native-tool presence/ordering (e.g. "bash").
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(t.TempDir())
	for _, def := range builtins.Defaults() {
		r.Register(def)
	}
	return r
}

func TestCompositeDefinitionsStableOrder(t *testing.T) {
	extA := &fakeStaticExt{name: "alpha", specs: []core.ToolSpec{spec("a_tool", "extension:alpha", "a")}}
	extB := &fakeStaticExt{name: "beta", specs: []core.ToolSpec{spec("b_tool", "extension:beta", "b")}}
	c := newTestComposite(t, extA, extB)
	c.BeginTurn(context.Background())

	first, err := json.Marshal(c.Definitions())
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	second, err := json.Marshal(c.Definitions())
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Definitions() not stable across calls:\n%s\nvs\n%s", first, second)
	}

	defs := c.Definitions()
	if len(defs) == 0 {
		t.Fatal("no definitions")
	}
	// order: native first, then extensions by registration order
	idx := map[string]int{}
	for i, d := range defs {
		idx[d.Name] = i
	}
	if idx["a_tool"] >= idx["b_tool"] {
		t.Errorf("extension registration order not preserved: a_tool@%d, b_tool@%d", idx["a_tool"], idx["b_tool"])
	}
	if idx["bash"] >= idx["a_tool"] {
		t.Errorf("native tools should precede extension tools: bash@%d, a_tool@%d", idx["bash"], idx["a_tool"])
	}
}

func TestCompositeOrigin(t *testing.T) {
	ext := &fakeStaticExt{name: "blackboard", specs: []core.ToolSpec{spec("repl", "extension:blackboard", "ok")}}
	c := newTestComposite(t, ext)
	c.BeginTurn(context.Background())

	if got := c.Origin("repl"); got != "extension:blackboard" {
		t.Errorf("Origin(repl) = %q, want %q", got, "extension:blackboard")
	}
	if got := c.Origin("bash"); got != "native" {
		t.Errorf("Origin(bash) = %q, want %q", got, "native")
	}
}

func TestCompositeNameCollision(t *testing.T) {
	ext := &fakeStaticExt{name: "clash", specs: []core.ToolSpec{spec("bash", "extension:clash", "boom")}}
	_, err := NewComposite(newTestRegistry(t), core.NewExtensions(ext))
	if err == nil {
		t.Fatal("expected construction error on name collision with native tool")
	}
}

func TestCompositeExecuteRoutes(t *testing.T) {
	ext := &fakeStaticExt{name: "alpha", specs: []core.ToolSpec{spec("a_tool", "extension:alpha", "from-alpha")}}
	c := newTestComposite(t, ext)
	c.BeginTurn(context.Background())

	res := c.Execute(context.Background(), core.ToolCall{ID: "t1", Name: "a_tool", Input: "{}"})
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Output)
	}
	if res.Output != "from-alpha" {
		t.Errorf("Execute output = %q, want %q", res.Output, "from-alpha")
	}
	if res.ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want %q", res.ToolUseID, "t1")
	}
}

func TestCompositeDynamicSnapshotAtBeginTurn(t *testing.T) {
	dyn := &fakeDynamicExt{name: "mcp"}
	dyn.set([]core.ToolSpec{spec("dyn_one", "mcp:server", "one")})
	c := newTestComposite(t, dyn)

	ctx := context.Background()
	c.BeginTurn(ctx)
	if !hasDef(c.Definitions(), "dyn_one") {
		t.Fatal("dyn_one missing after first BeginTurn")
	}

	dyn.set([]core.ToolSpec{spec("dyn_two", "mcp:server", "two")})
	// change must NOT be visible until the next BeginTurn
	if hasDef(c.Definitions(), "dyn_two") {
		t.Fatal("dyn_two visible before next BeginTurn")
	}
	if !hasDef(c.Definitions(), "dyn_one") {
		t.Fatal("dyn_one dropped before next BeginTurn")
	}

	c.BeginTurn(ctx)
	if !hasDef(c.Definitions(), "dyn_two") {
		t.Fatal("dyn_two missing after second BeginTurn")
	}
	if hasDef(c.Definitions(), "dyn_one") {
		t.Fatal("dyn_one still present after second BeginTurn")
	}
}

func hasDef(defs []common.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}
