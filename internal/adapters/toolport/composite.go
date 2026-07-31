package toolport

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Composite is the harness ToolPort: it mounts the native tool registry,
// static extension tool bundles (core.ToolProvider), and dynamic sources
// (core.DynamicToolSource, snapshotted at each BeginTurn). Definition order
// is stable: native (sorted by name), then extensions in registration order,
// then dynamic sources in registration order.
type Composite struct {
	static  []core.ToolSpec
	sources []core.DynamicToolSource

	mu      sync.RWMutex
	dynamic []core.ToolSpec          // snapshot from the last BeginTurn
	byName  map[string]core.ToolSpec // lowercased name → spec (static + snapshot)
}

// NewComposite builds a Composite from the native registry and the registered
// extensions. A static extension tool whose name collides with a native tool
// (or another extension's) is a construction error.
func NewComposite(native *Registry, exts *core.Extensions) (*Composite, error) {
	var static []core.ToolSpec

	// native tools: wrap into ToolSpecs closing over the registry. The
	// registry stores tools in a map, so sort by name for a stable order.
	nativeDefs := native.ProviderDefinitions()
	sort.Slice(nativeDefs, func(i, j int) bool { return nativeDefs[i].Name < nativeDefs[j].Name })
	for _, def := range nativeDefs {
		static = append(static, core.ToolSpec{
			Definition: def,
			Origin:     "native",
			ReadOnly:   native.IsReadOnly(def.Name),
			Execute:    nativeExecute(native),
		})
	}

	// extension tools, in registration order
	for _, p := range exts.ToolProviders() {
		static = append(static, p.Tools()...)
	}

	byName := make(map[string]core.ToolSpec, len(static))
	for _, s := range static {
		key := strings.ToLower(s.Definition.Name)
		if _, ok := byName[key]; ok {
			return nil, fmt.Errorf("tool name collision: %q already mounted", s.Definition.Name)
		}
		byName[key] = s
	}

	return &Composite{
		static:  static,
		sources: exts.DynamicToolSources(),
		byName:  byName,
	}, nil
}

// nativeExecute returns the Execute closure for registry-backed tools,
// converting the registry's (ToolResult, error) into a single core.ToolResult.
func nativeExecute(reg *Registry) func(ctx context.Context, call core.ToolCall) core.ToolResult {
	return func(ctx context.Context, call core.ToolCall) core.ToolResult {
		res, err := reg.Execute(ctx, call.Name, call.Input)
		if err != nil {
			return core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("tool execution failed: %v", err), IsError: true}
		}
		res.ToolUseID = call.ID
		return res
	}
}

// BeginTurn snapshots every dynamic source. Called by the core loop once at
// turn start; dynamic changes become visible only at the next BeginTurn.
func (c *Composite) BeginTurn(ctx context.Context) {
	var dynamic []core.ToolSpec
	byName := make(map[string]core.ToolSpec, len(c.static))
	for _, s := range c.static {
		byName[strings.ToLower(s.Definition.Name)] = s
	}
	for _, src := range c.sources {
		for _, s := range src.CurrentTools(ctx) {
			key := strings.ToLower(s.Definition.Name)
			if _, ok := byName[key]; ok {
				slog.Warn("dynamic tool name collision; skipping", "tool", s.Definition.Name, "origin", s.Origin)
				continue
			}
			byName[key] = s
			dynamic = append(dynamic, s)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.dynamic = dynamic
	c.byName = byName
}

// Definitions returns the mounted tool definitions in stable order.
func (c *Composite) Definitions() []common.ToolDefinition {
	c.mu.RLock()
	defer c.mu.RUnlock()
	defs := make([]common.ToolDefinition, 0, len(c.static)+len(c.dynamic))
	for _, s := range c.static {
		defs = append(defs, s.Definition)
	}
	for _, s := range c.dynamic {
		defs = append(defs, s.Definition)
	}
	return defs
}

// Origin returns the mount origin for a tool name; unknown names report
// "native" (the loop treats origin as advisory).
func (c *Composite) Origin(name string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s, ok := c.byName[strings.ToLower(name)]; ok {
		return s.Origin
	}
	return "native"
}

// ReadOnly reports whether the named mounted tool declares itself read-only.
// Unknown names report false (treated as mutating).
func (c *Composite) ReadOnly(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.byName[strings.ToLower(name)]
	return ok && s.ReadOnly
}

// Execute routes the call to the owning source's Execute func. Panics are
// recovered into error results here — the single choke point for all mounts.
func (c *Composite) Execute(ctx context.Context, call core.ToolCall) (result core.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool panicked", "tool", call.Name, "panic", r, "stack", string(debug.Stack()))
			result = core.ToolResult{ToolUseID: call.ID, Output: fmt.Sprintf("tool %q panicked: %v", call.Name, r), IsError: true}
		}
	}()

	c.mu.RLock()
	s, ok := c.byName[strings.ToLower(call.Name)]
	var available []string
	if !ok {
		available = make([]string, 0, len(c.byName))
		for n := range c.byName {
			available = append(available, n)
		}
	}
	c.mu.RUnlock()
	if !ok {
		sort.Strings(available)
		return core.ToolResult{
			ToolUseID: call.ID,
			Output:    fmt.Sprintf("Tool %q not found. Available tools: %s", call.Name, strings.Join(available, ", ")),
			IsError:   true,
		}
	}
	return s.Execute(ctx, call)
}
