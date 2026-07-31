package todo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*TodoCreateTool)(nil)

type TodoCreateTool struct {
	file *TodoFile
}

func NewTodoCreateTool(f *TodoFile) *TodoCreateTool {
	return &TodoCreateTool{file: f}
}

func (t *TodoCreateTool) Name() string { return "TodoCreate" }

func (t *TodoCreateTool) Description() string {
	return "Add a single task to the existing plan without replacing it. " +
		"Use when you need to add work mid-execution. " +
		"Input: description (required), depends_on (optional array of task IDs), priority (optional, default medium)."
}

func (t *TodoCreateTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"description": {Type: tooldef.JsonTypeString},
			"depends_on":  {Type: tooldef.JsonTypeArray, Items: &tooldef.SchemaProperty{Type: tooldef.JsonTypeString}},
			"priority":    {Type: tooldef.JsonTypeString},
		},
		Required: []string{"description"},
	}
}

func (t *TodoCreateTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("description is required", tooldef.WithError()), nil
	}

	var input struct {
		Description string       `json:"description"`
		DependsOn   []string     `json:"depends_on"`
		Priority    TaskPriority `json:"priority"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Description == "" {
		return tooldef.NewToolResult("description cannot be empty", tooldef.WithError()), nil
	}

	task, err := t.file.CreateTask(input.Description, input.DependsOn, input.Priority)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("create failed: %v", err), tooldef.WithError()), nil
	}

	data, _ := json.Marshal(task)
	return tooldef.NewToolResult(string(data)), nil
}
