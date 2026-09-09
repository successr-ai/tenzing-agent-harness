package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*WriteTool)(nil)

// WriteTool creates or overwrites a file. Parent directories are created as
// needed; writes are atomic (temp file + rename). With a FileTracker,
// overwriting a file the session has not Read (or that changed since) is
// rejected.
type WriteTool struct {
	tracker *FileTracker
}

// NewWriteTool returns a WriteTool enforcing read-before-overwrite via
// tracker. A nil tracker disables enforcement.
func NewWriteTool(tracker *FileTracker) *WriteTool {
	return &WriteTool{tracker: tracker}
}

func (t *WriteTool) Name() string { return "Write" }

func (t *WriteTool) Description() string {
	return "Write content to a file, creating it (and parent directories) if needed. " +
		"Overwriting an existing file requires Reading it first."
}

func (t *WriteTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"file_path": {Type: tooldef.JsonTypeString},
			"content":   {Type: tooldef.JsonTypeString},
		},
		Required: []string{"file_path", "content"},
	}
}

func (t *WriteTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("file_path and content are required", tooldef.WithError()), nil
	}

	var input struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.FilePath == "" {
		return tooldef.NewToolResult("file_path is required", tooldef.WithError()), nil
	}

	path := resolvePath(exctx.WorkingDir, input.FilePath)
	defer lockPath(path)()

	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if t.tracker != nil {
			if verr := t.tracker.Verify(path, existing); verr != nil {
				return tooldef.NewToolResult(fmt.Sprintf("cannot overwrite %s: %v", path, verr), tooldef.WithError()), nil
			}
		}
	case os.IsNotExist(err):
		// new file — nothing to verify
	default:
		return tooldef.NewToolResult(fmt.Sprintf("cannot check existing file: %v", err), tooldef.WithError()), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot create directory: %v", err), tooldef.WithError()), nil
	}
	if err := writeFileAtomic(path, []byte(input.Content)); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot write file: %v", err), tooldef.WithError()), nil
	}
	if t.tracker != nil {
		t.tracker.Record(path, []byte(input.Content))
	}

	// existing is nil when the file did not exist: DiffFiles then reports
	// the whole content as additions.
	diff := DiffFiles(input.FilePath, existing, []byte(input.Content))
	return tooldef.NewToolResult("File written: "+path+" "+diff.Summary(), tooldef.WithMetadata(diffMetadata(diff))), nil
}
