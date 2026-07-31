package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*LoadSkillTool)(nil)

type SkillContentLoader interface {
	Load(name string) (string, error)
}

type LoadSkillTool struct {
	loader SkillContentLoader
}

func NewLoadSkillTool(loader SkillContentLoader) *LoadSkillTool {
	return &LoadSkillTool{loader: loader}
}

func (t *LoadSkillTool) Name() string { return "load_skill" }

func (t *LoadSkillTool) Description() string {
	return "Load full instructions for a named skill. Call list_skills first to see available skills. Do NOT guess skill names."
}

func (t *LoadSkillTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"name": {Type: tooldef.JsonTypeString},
		},
		Required: []string{"name"},
	}
}

func (t *LoadSkillTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	var args struct {
		Name string `json:"name"`
	}
	if len(exctx.Arguments) == 0 {
		return tooldef.NewToolResult("missing arguments", tooldef.WithError()), nil
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &args); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid arguments: %v", err), tooldef.WithError()), nil
	}
	content, err := t.loader.Load(args.Name)
	if err != nil {
		return tooldef.NewToolResult(err.Error(), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(content), nil
}
