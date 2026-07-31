// Package nexustools exposes nexus channels to the agent as tools. The
// tools live beside nexus (not in harness builtins) and are injected via
// harness.WithTool.
package nexustools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*ListChannelsTool)(nil)

type ListChannelsTool struct {
	nexus *nexus.Nexus
}

func NewListChannelsTool(n *nexus.Nexus) *ListChannelsTool {
	return &ListChannelsTool{nexus: n}
}

func (t *ListChannelsTool) Name() string { return "list_channels" }

func (t *ListChannelsTool) Description() string {
	return "List all nexus input channels (external log/message streams wired into this app) with their type, status, buffered message count, and error count. Use this first to discover what debug data is available, then read_channel or search_channel to inspect a channel."
}

func (t *ListChannelsTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{},
		Required:   []string{},
	}
}

func (t *ListChannelsTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	infos := t.nexus.ChannelInfos()
	b, err := json.Marshal(infos)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("marshal channels: %v", err), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(string(b)), nil
}
