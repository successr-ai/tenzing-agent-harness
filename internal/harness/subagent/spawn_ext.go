package subagent

import (
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

// spawnExtOrigin tags the spawn_agent tool's mount origin.
const spawnExtOrigin = "extension:subagents"

var (
	_ core.Extension    = (*SpawnExt)(nil)
	_ core.ToolProvider = (*SpawnExt)(nil)
)

// SpawnExt mounts the spawn_agent tool as a core.Extension
// (core.ToolProvider). It lives in this package — not a separate
// extensions/subagentext package — because the factory's own child wiring
// needs to build it for grandchildren, which would be an import cycle.
// Depth exclusion is a wiring decision: the composition root (and
// childExtensions, for children) omits this extension for runners at max
// depth, so the tool never appears in their surface.
type SpawnExt struct {
	factory AgentFactory
}

// NewSpawnExt wraps a subagent factory (typically *SubAgentFactory, which
// owns child construction, events, and blackboard deposit).
func NewSpawnExt(factory AgentFactory) *SpawnExt {
	return &SpawnExt{factory: factory}
}

func (e *SpawnExt) Name() string { return "subagents" }

// Tools wraps the existing spawn_agent tool implementation — same behavior,
// new mount path through the composite ToolPort.
func (e *SpawnExt) Tools() []core.ToolSpec {
	return []core.ToolSpec{
		tooldef.SpecFromDefinition(NewSpawnAgentTool(e.factory), spawnExtOrigin),
	}
}
