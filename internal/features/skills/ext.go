package skills

import (
	"fmt"
	"sort"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

const origin = "extension:skills"

var (
	_ core.Extension         = (*Ext)(nil)
	_ core.ToolProvider      = (*Ext)(nil)
	_ core.PromptContributor = (*Ext)(nil)
)

// Ext surfaces the skills registry as a core.Extension: the
// list_skills/load_skill tools via core.ToolProvider and the skills index
// block via core.PromptContributor. The agent itself knows nothing about
// skills — the composition root registers this extension and assembles the
// system prompt from PromptFragments.
type Ext struct {
	reg *Registry
}

func NewExt(reg *Registry) *Ext {
	return &Ext{reg: reg}
}

func (e *Ext) Name() string { return "skills" }

// Tools wraps the existing tooldef implementations — same behavior, new
// mount path through the composite ToolPort.
func (e *Ext) Tools() []core.ToolSpec {
	return []core.ToolSpec{
		tooldef.SpecFromDefinition(NewListSkillsTool(e.reg), origin),
		tooldef.SpecFromDefinition(NewLoadSkillTool(e.reg), origin),
	}
}

// PromptFragment is the "Available skills…" index block, moved verbatim from
// the agent's old buildAgentSystemPrompt — with the skill lines sorted by
// name so the fragment is byte-stable across runs (prompt cache friendly).
func (e *Ext) PromptFragment() string {
	skillMap := e.reg.GetSkillMap()
	if len(skillMap) == 0 {
		return ""
	}
	names := make([]string, 0, len(skillMap))
	for name := range skillMap {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Available skills (call load_skill to get full instructions):")
	for _, name := range names {
		fmt.Fprintf(&b, "\n- %s: %s", name, skillMap[name])
	}
	b.WriteString("\nWhen a task requires specialised knowledge, call load_skill(name) to get full instructions before starting. Do NOT guess.")
	return b.String()
}
