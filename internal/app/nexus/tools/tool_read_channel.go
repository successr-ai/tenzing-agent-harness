package nexustools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

const defaultLastN = 100

var _ tooldef.Definition = (*ReadChannelTool)(nil)

type ReadChannelTool struct {
	nexus *nexus.Nexus
}

func NewReadChannelTool(n *nexus.Nexus) *ReadChannelTool {
	return &ReadChannelTool{nexus: n}
}

func (t *ReadChannelTool) Name() string { return "read_channel" }

func (t *ReadChannelTool) Description() string {
	return "Read the most recent buffered messages from a nexus input channel. Set errors_only to true to see only lines that matched the channel's error pattern. Use list_channels to discover channel names."
}

func (t *ReadChannelTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"name":        {Type: tooldef.JsonTypeString},
			"last_n":      {Type: tooldef.JsonTypeInteger},
			"errors_only": {Type: tooldef.JsonTypeBoolean},
		},
		Required: []string{"name"},
	}
}

func (t *ReadChannelTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("name is required", tooldef.WithError()), nil
	}
	var input struct {
		Name       string `json:"name"`
		LastN      *int   `json:"last_n"`
		ErrorsOnly bool   `json:"errors_only"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Name == "" {
		return tooldef.NewToolResult("name is required", tooldef.WithError()), nil
	}
	lastN := defaultLastN
	if input.LastN != nil && *input.LastN > 0 {
		lastN = *input.LastN
	}

	entries, err := t.nexus.Read(input.Name, lastN, input.ErrorsOnly)
	if err != nil {
		return tooldef.NewToolResult(err.Error(), tooldef.WithError()), nil
	}
	return tooldef.NewToolResult(formatEntries(input.Name, entries)), nil
}

// formatEntries renders entries one per line: [seq] timestamp [ERR] text
func formatEntries(channel string, entries []nexus.Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d matching entries in channel %q:\n", len(entries), channel)
	for _, e := range entries {
		flag := ""
		if e.IsError {
			flag = " [ERR]"
		}
		fmt.Fprintf(&sb, "[%d] %s%s %s\n", e.Seq, e.Time.Format("15:04:05.000"), flag, e.Text)
	}
	return sb.String()
}
