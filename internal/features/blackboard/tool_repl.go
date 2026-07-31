package blackboard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var (
	_ tooldef.Definition       = (*REPLTool)(nil)
	_ tooldef.ReadOnlyReporter = (*REPLTool)(nil)
)

// REPLTool exposes the shared blackboard to one agent. Each agent gets its
// own instance bound to its slot ID, since ExecutionContext carries no
// caller identity.
type REPLTool struct {
	bb      *Blackboard
	agentID string
}

// NewREPLTool binds the shared blackboard to the given agent slot.
func NewREPLTool(bb *Blackboard, agentID string) *REPLTool {
	return &REPLTool{bb: bb, agentID: agentID}
}

func (t *REPLTool) Name() string { return "repl" }

// ReadOnly reports true: the sandbox blocks open and non-allowlisted
// imports, its file callbacks (read_file, grep_file, list_files) only
// read, and Landlock enforces read-only filesystem access on Linux. Only
// shared in-memory blackboard state mutates, which read-only mode
// explicitly tolerates (see WithReadOnly).
func (t *REPLTool) ReadOnly() bool { return true }

func (t *REPLTool) Description() string {
	return fmt.Sprintf(
		"Execute Python in the persistent shared REPL (the blackboard). State persists across calls "+
			"and is shared with other agents. The dict `bb` holds every agent's results: "+
			"bb['<agent_id>']['result'] is a completed sub-agent's full output. "+
			"Writes are enforced: creating or replacing a top-level bb key other than bb['%s'] (your slot) "+
			"raises PermissionError. Read anything; never busy-wait on "+
			"another agent's slot. Helpers: peek(s, start, n) pages any string; bb_grep(pattern, s) "+
			"greps it with line numbers; llm_query(prompt) and llm_batch(prompts) call a sub-LLM. "+
			"Output longer than %d chars is truncated — print slices or peek() to see more. "+
			"llm_query blocks the shared REPL for all agents while it runs — keep individual calls "+
			"small and prefer llm_batch for fan-out work. "+
			"If a repl call fails with a transport error or is cancelled mid-execution, the blackboard "+
			"resets and all bb contents are lost — if a slot you expect is missing, re-derive it or "+
			"re-spawn its producer.",
		t.agentID, DefaultHeadChars+DefaultTailChars)
}

func (t *REPLTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"code": {Type: tooldef.JsonTypeString},
		},
		Required: []string{"code"},
	}
}

func (t *REPLTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("code is required", tooldef.WithError()), nil
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Code == "" {
		return tooldef.NewToolResult("code is required", tooldef.WithError()), nil
	}
	stdout, err := t.bb.Execute(ctx, t.agentID, input.Code)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("repl error: %v", err), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(truncateOutput(stdout, t.bb.HeadChars(), t.bb.TailChars())), nil
}
