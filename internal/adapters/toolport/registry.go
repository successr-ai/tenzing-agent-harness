package toolport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type ToolProvider interface {
	GetTools() []tooldef.Definition
}

type Registry struct {
	tools      map[string]tooldef.Definition
	workingDir string
}

// NewRegistry creates an empty Registry. Callers register tools via
// Register (e.g. seed the standard set with builtins.Defaults()).
// workingDir, when non-empty, is passed to tools via ExecutionContext so
// relative paths resolve against it; empty means each tool falls back to
// the process working directory.
func NewRegistry(workingDir string) *Registry {
	return &Registry{
		tools:      make(map[string]tooldef.Definition),
		workingDir: workingDir,
	}
}

func (r *Registry) Register(def tooldef.Definition) error {
	name := strings.ToLower(def.Name())
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tool already registered")
	}
	r.tools[name] = def
	return nil
}

func (r *Registry) RegisterFromProvider(provider ToolProvider) error {
	tools := provider.GetTools()
	for _, def := range tools {
		if err := r.Register(def); err != nil {
			return fmt.Errorf("unable to register tool: %w", err)
		}
	}
	return nil
}

// CopyWithout copies the tools in the Registry without certain ones
func (r *Registry) CopyWithout(names ...string) *Registry {
	exclude := make(map[string]struct{}, len(names))
	for _, n := range names {
		exclude[strings.ToLower(n)] = struct{}{}
	}
	filtered := &Registry{
		tools:      make(map[string]tooldef.Definition, len(r.tools)),
		workingDir: r.workingDir,
	}
	for name, def := range r.tools {
		if _, skip := exclude[name]; !skip {
			filtered.tools[name] = def
		}
	}
	return filtered
}

func (r *Registry) Definitions() []tooldef.Definition {
	defs := make([]tooldef.Definition, 0, len(r.tools))
	for _, d := range r.tools {
		defs = append(defs, d)
	}
	return defs
}

func (r *Registry) ProviderDefinitions() []common.ToolDefinition {
	defs := r.Definitions()
	providerDefs := make([]common.ToolDefinition, len(defs))
	for i, d := range defs {
		schema, _ := json.Marshal(d.Schema())
		providerDefs[i] = common.ToolDefinition{
			Name:        d.Name(),
			Description: d.Description(),
			InputSchema: schema,
		}
	}
	return providerDefs
}

// func (r *Registry) WorkingDir() string {
// 	return r.workingDir
// }

// IsReadOnly reports whether the named tool declares itself read-only via
// tooldef.ReadOnlyReporter. Unknown tools report false.
func (r *Registry) IsReadOnly(name string) bool {
	def, ok := r.tools[strings.ToLower(name)]
	return ok && tooldef.IsReadOnly(def)
}

func (r *Registry) Execute(ctx context.Context, name string, input string) (core.ToolResult, error) {
	toolDef, ok := r.tools[strings.ToLower(name)]
	if !ok {
		available := make([]string, 0, len(r.tools))
		for n := range r.tools {
			available = append(available, n)
		}
		slog.Warn("unknown tool called", "tool", name, "available", available)
		return core.ToolResult{
			Output:  fmt.Sprintf("Tool %q not found. Available tools: %s", name, strings.Join(available, ", ")),
			IsError: true,
		}, nil
	}
	exctx := tooldef.ExecutionContext{
		Arguments:  []string{input},
		WorkingDir: r.workingDir,
	}
	result, err := toolDef.Execute(ctx, exctx)
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("error executing tool %s: %w", name, err)
	}
	return result, nil
}
