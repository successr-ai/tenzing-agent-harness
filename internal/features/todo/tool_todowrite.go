package todo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*TodoWriteTool)(nil)

type TodoWriteTool struct {
	file *TodoFile
}

func NewTodoWriteTool(f *TodoFile) *TodoWriteTool {
	return &TodoWriteTool{file: f}
}

func (t *TodoWriteTool) Name() string { return "TodoWrite" }

func (t *TodoWriteTool) Description() string {
	return "Write a plan of tasks before executing. ALWAYS call this tool first before starting any multi-step work. " +
		"Input: tasks array. Each task has a 'task' string (required), optional 'depends_on' array of indices (0-based, " +
		"referencing other tasks in this list), and optional 'priority' (high/medium/low, default medium). " +
		"Your plan persists for the lifetime of this session and survives context compression."
}

func (t *TodoWriteTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"tasks": {
				Type:  tooldef.JsonTypeArray,
				Items: &tooldef.SchemaProperty{Type: tooldef.JsonTypeObject},
			},
		},
		Required: []string{"tasks"},
	}
}

type todoWriteInput struct {
	Tasks []struct {
		Task      string       `json:"task"`
		DependsOn []int        `json:"depends_on"`
		Priority  TaskPriority `json:"priority"`
	} `json:"tasks"`
}

func (t *TodoWriteTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("tasks argument is required", tooldef.WithError()), nil
	}

	var input todoWriteInput
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid JSON: %v", err), tooldef.WithError()), nil
	}
	if len(input.Tasks) == 0 {
		return tooldef.NewToolResult("tasks list cannot be empty", tooldef.WithError()), nil
	}

	tasks := make([]Task, len(input.Tasks))
	for i, item := range input.Tasks {
		if item.Task == "" {
			return tooldef.NewToolResult(fmt.Sprintf("task at index %d has empty description", i), tooldef.WithError()), nil
		}
		priority := item.Priority
		if priority == "" {
			priority = PriorityMedium
		}
		tasks[i] = Task{
			ID:          randomID(8),
			Description: item.Task,
			Status:      "pending",
			Priority:    priority,
			DependsOn:   []string{},
		}
	}

	for i, item := range input.Tasks {
		for _, depIdx := range item.DependsOn {
			if depIdx < 0 || depIdx >= len(tasks) {
				return tooldef.NewToolResult(fmt.Sprintf("depends_on index %d out of range at task %d", depIdx, i), tooldef.WithError()), nil
			}
			if depIdx == i {
				return tooldef.NewToolResult(fmt.Sprintf("task %d cannot depend on itself", i), tooldef.WithError()), nil
			}
			tasks[i].DependsOn = append(tasks[i].DependsOn, tasks[depIdx].ID)
		}
	}

	if err := t.file.WriteTasks(tasks); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("failed to write: %v", err), tooldef.WithError()), nil
	}

	if t.file.emitter != nil {
		for _, task := range tasks {
			t.file.emitter.Emit(core.TaskCreatedEvent{
				BaseEvent:   core.NewBaseEvent(core.EventTaskCreated, ""),
				TaskID:      task.ID,
				Description: task.Description,
			})
		}
	}

	return tooldef.NewToolResult(fmt.Sprintf("Plan written: %d tasks", len(tasks))), nil
}
