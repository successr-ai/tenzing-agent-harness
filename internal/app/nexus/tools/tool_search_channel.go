package nexustools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*SearchChannelTool)(nil)

type SearchChannelTool struct {
	nexus *nexus.Nexus
}

func NewSearchChannelTool(n *nexus.Nexus) *SearchChannelTool {
	return &SearchChannelTool{nexus: n}
}

func (t *SearchChannelTool) Name() string { return "search_channel" }

func (t *SearchChannelTool) Description() string {
	return "Search a nexus input channel's buffered messages with a Go regex pattern. Returns the most recent matches. Use list_channels to discover channel names; use read_channel to page through recent messages without a pattern."
}

func (t *SearchChannelTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"name":    {Type: tooldef.JsonTypeString},
			"pattern": {Type: tooldef.JsonTypeString},
			"last_n":  {Type: tooldef.JsonTypeInteger},
		},
		Required: []string{"name", "pattern"},
	}
}

func (t *SearchChannelTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("name and pattern are required", tooldef.WithError()), nil
	}
	var input struct {
		Name    string `json:"name"`
		Pattern string `json:"pattern"`
		LastN   *int   `json:"last_n"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Name == "" || input.Pattern == "" {
		return tooldef.NewToolResult("name and pattern are required", tooldef.WithError()), nil
	}
	lastN := defaultLastN
	if input.LastN != nil && *input.LastN > 0 {
		lastN = *input.LastN
	}

	entries, err := t.nexus.Search(input.Name, input.Pattern, lastN)
	if err != nil {
		return tooldef.NewToolResult(err.Error(), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(formatEntries(input.Name, entries)), nil
}
