package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

// maxLsEntries caps ls output so huge directories don't flood the context.
const maxLsEntries = 500

var _ tooldef.Definition = (*LsTool)(nil)

type LsTool struct{}

func (t *LsTool) Name() string { return "ls" }

// ReadOnly marks ls as safe for concurrent execution within a tool batch.
func (t *LsTool) ReadOnly() bool { return true }

func (t *LsTool) Description() string {
	return "List directory entries, sorted by name. Directories end with '/'; files show their size. " +
		"path defaults to the working directory."
}

func (t *LsTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"path": {Type: tooldef.JsonTypeString},
		},
		Required: []string{},
	}
}

func (t *LsTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	var input struct {
		Path string `json:"path"`
	}
	if len(exctx.Arguments) > 0 && exctx.Arguments[0] != "" {
		if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
			return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
		}
	}
	if input.Path == "" {
		input.Path = "."
	}

	path := resolvePath(exctx.WorkingDir, input.Path)
	entries, err := os.ReadDir(path) // sorted by filename
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot list directory: %v", err), tooldef.WithError()), nil
	}
	if len(entries) == 0 {
		return tooldef.NewToolResult(path + ": empty directory"), nil
	}

	total := len(entries)
	truncated := total > maxLsEntries
	if truncated {
		entries = entries[:maxLsEntries]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:\n", path)
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&sb, "%s/\n", e.Name())
			continue
		}
		size := int64(-1)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		if size >= 0 {
			fmt.Fprintf(&sb, "%s  %dB\n", e.Name(), size)
		} else {
			fmt.Fprintf(&sb, "%s\n", e.Name())
		}
	}
	if truncated {
		fmt.Fprintf(&sb, "[showing %d of %d entries]\n", maxLsEntries, total)
	}
	return tooldef.NewToolResult(strings.TrimRight(sb.String(), "\n")), nil
}
