package todo

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*TodoReadTool)(nil)

type TodoReadTool struct {
	file *TodoFile
}

func NewTodoReadTool(f *TodoFile) *TodoReadTool {
	return &TodoReadTool{file: f}
}

func (t *TodoReadTool) Name() string { return "TodoRead" }

func (t *TodoReadTool) Description() string {
	return "Read the current plan and check progress. Returns all tasks in dependency order with their status."
}

func (t *TodoReadTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{},
		Required:   []string{},
	}
}

func (t *TodoReadTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	reminder := t.file.FormatReminder()
	if reminder == "" {
		return tooldef.NewToolResult("(no plan — call TodoWrite first)"), nil
	}
	return tooldef.NewToolResult(reminder), nil
}
