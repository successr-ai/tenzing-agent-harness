package todo

import (
	"context"
	"encoding/json"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*TodoNextTool)(nil)

type TodoNextTool struct {
	file *TodoFile
}

func NewTodoNextTool(f *TodoFile) *TodoNextTool {
	return &TodoNextTool{file: f}
}

func (t *TodoNextTool) Name() string { return "TodoNext" }

func (t *TodoNextTool) Description() string {
	return "Get the next task to work on. Returns the highest-priority pending task " +
		"whose dependencies are all done. Use after completing a task to find what to do next."
}

func (t *TodoNextTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{},
		Required:   []string{},
	}
}

func (t *TodoNextTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	task, ok, err := t.file.NextTask()
	if err != nil {
		return tooldef.NewToolResult("no plan found — call TodoWrite first", tooldef.WithError()), nil
	}
	if !ok {
		return tooldef.NewToolResult("(no unblocked tasks available)"), nil
	}

	data, _ := json.Marshal(task)
	return tooldef.NewToolResult(string(data)), nil
}
