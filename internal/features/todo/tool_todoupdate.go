package todo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*TodoUpdateTool)(nil)

type TodoUpdateTool struct {
	file *TodoFile
}

func NewTodoUpdateTool(f *TodoFile) *TodoUpdateTool {
	return &TodoUpdateTool{file: f}
}

func (t *TodoUpdateTool) Name() string { return "TodoUpdate" }

func (t *TodoUpdateTool) Description() string {
	return "Update the status of a task by ID (or ID prefix). " +
		"Statuses: pending, in_progress, done. Optionally set a result string."
}

func (t *TodoUpdateTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"id":     {Type: tooldef.JsonTypeString},
			"status": {Type: tooldef.JsonTypeString},
			"result": {Type: tooldef.JsonTypeString},
		},
		Required: []string{"id", "status"},
	}
}

func (t *TodoUpdateTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("id and status are required", tooldef.WithError()), nil
	}

	var input struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid JSON: %v", err), tooldef.WithError()), nil
	}
	if input.ID == "" || input.Status == "" {
		return tooldef.NewToolResult("id and status cannot be empty", tooldef.WithError()), nil
	}

	if err := t.file.UpdateTask(input.ID, input.Status, input.Result); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("update failed: %v", err), tooldef.WithError()), nil
	}

	_, err := t.file.ReadTasks()
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("Task updated but read failed: %v", err), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(fmt.Sprintf("Task %s → %s\n\n%s", input.ID, input.Status, t.file.FormatReminder())), nil
}
