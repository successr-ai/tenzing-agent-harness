package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

const (
	defaultReadLimit = 2000
)

var _ tooldef.Definition = (*ReadTool)(nil)

type ReadTool struct {
	tracker *FileTracker
}

// NewReadTool returns a ReadTool that stamps read content into tracker so
// Edit/Write can verify freshness. A nil tracker disables stamping.
func NewReadTool(tracker *FileTracker) *ReadTool {
	return &ReadTool{tracker: tracker}
}

func (t *ReadTool) Name() string { return "Read" }

// ReadOnly marks Read as safe for concurrent execution within a tool batch.
func (t *ReadTool) ReadOnly() bool { return true }

func (t *ReadTool) Description() string {
	return "Read a file and return its contents with line numbers."
}

func (t *ReadTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"file_path": {Type: tooldef.JsonTypeString},
			"limit":     {Type: tooldef.JsonTypeNumber},
			"offset":    {Type: tooldef.JsonTypeNumber},
		},
		Required: []string{"file_path"},
	}
}

func (t *ReadTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("file_path is required", tooldef.WithError()), nil
	}

	var input struct {
		FilePath string `json:"file_path"`
		Limit    *int   `json:"limit"`
		Offset   *int   `json:"offset"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.FilePath == "" {
		return tooldef.NewToolResult("file_path is required", tooldef.WithError()), nil
	}

	filePath := input.FilePath
	offset := 0
	limit := defaultReadLimit
	if input.Limit != nil {
		if *input.Limit < 0 {
			return tooldef.NewToolResult("limit must be a non-negative integer", tooldef.WithError()), nil
		}
		limit = *input.Limit
	}
	if input.Offset != nil {
		if *input.Offset < 0 {
			return tooldef.NewToolResult("offset must be a non-negative integer", tooldef.WithError()), nil
		}
		offset = *input.Offset
	}

	resolved := resolvePath(exctx.WorkingDir, filePath)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot read file: %v", err), tooldef.WithError()), nil
	}
	// Stamp the whole file even for offset/limit reads: the tracker is a
	// freshness guarantee, not a full-knowledge one (see FileTracker docs).
	if t.tracker != nil {
		t.tracker.Record(resolved, data)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)

	if offset > totalLines {
		offset = totalLines
	}
	visible := lines[offset:]
	truncated := false
	if limit > 0 && len(visible) > limit {
		visible = visible[:limit]
		truncated = true
	}

	var sb strings.Builder
	for i, line := range visible {
		lineNum := offset + i + 1
		fmt.Fprintf(&sb, "%6d\t%s\n", lineNum, line)
	}

	endLine := offset + len(visible)
	if truncated {
		fmt.Fprintf(&sb, "\n[Showing lines %d-%d of %d. For full-file analysis use the repl tool (read_file + llm_query); to page use Read with offset=%d.]",
			offset+1, endLine, totalLines, endLine)
	} else {
		fmt.Fprintf(&sb, "\n[%d lines]", totalLines)
	}

	return tooldef.NewToolResult(sb.String(), tooldef.WithMetadata(map[string]string{
		"limit":  strconv.Itoa(limit),
		"offset": strconv.Itoa(offset),
		"total":  strconv.Itoa(totalLines),
		"fp":     filePath,
	})), nil
}
