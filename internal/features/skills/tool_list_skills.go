package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*ListSkillsTool)(nil)

type SkillLister interface {
	GetSkillMap() map[string]string
}

type ListSkillsTool struct {
	lister SkillLister
}

func NewListSkillsTool(lister SkillLister) *ListSkillsTool {
	return &ListSkillsTool{lister: lister}
}

func (t *ListSkillsTool) Name() string { return "list_skills" }

func (t *ListSkillsTool) Description() string {
	return "List all available skills. Call this to see what specialized knowledge is available before starting a task."
}

func (t *ListSkillsTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{},
		Required:   []string{},
	}
}

func (t *ListSkillsTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	skills := t.lister.GetSkillMap()
	if len(skills) == 0 {
		return tooldef.NewToolResult("No skills available."), nil
	}
	var lines []string
	for name, desc := range skills {
		lines = append(lines, fmt.Sprintf("- %s: %s", name, desc))
	}
	return tooldef.NewToolResult(strings.Join(lines, "\n")), nil
}
