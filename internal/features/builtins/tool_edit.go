package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

var _ tooldef.Definition = (*EditTool)(nil)

type EditTool struct {
	tracker *FileTracker
}

// NewEditTool returns an EditTool enforcing read-before-edit via tracker.
// A nil tracker disables enforcement.
func NewEditTool(tracker *FileTracker) *EditTool {
	return &EditTool{tracker: tracker}
}

func (t *EditTool) Name() string { return "Edit" }

func (t *EditTool) Description() string {
	return "Replace a string in a file. The file must have been Read first. " +
		"Fails if old_string is not found or is not unique (unless replace_all=true)."
}

func (t *EditTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"file_path":   {Type: tooldef.JsonTypeString},
			"old_string":  {Type: tooldef.JsonTypeString},
			"new_string":  {Type: tooldef.JsonTypeString},
			"replace_all": {Type: tooldef.JsonTypeBoolean},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

func (t *EditTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("file_path, old_string, and new_string are required", tooldef.WithError()), nil
	}

	var input struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.FilePath == "" || input.OldString == "" {
		return tooldef.NewToolResult("file_path and old_string are required", tooldef.WithError()), nil
	}

	filePath := resolvePath(exctx.WorkingDir, input.FilePath)
	oldString := input.OldString
	newString := input.NewString
	replaceAll := input.ReplaceAll

	// Hold the path lock across read-verify-write so the verified content is
	// the content the write is based on.
	defer lockPath(filePath)()

	data, err := os.ReadFile(filePath)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot read file: %v", err), tooldef.WithError()), nil
	}

	if t.tracker != nil {
		if verr := t.tracker.Verify(filePath, data); verr != nil {
			return tooldef.NewToolResult(fmt.Sprintf("cannot edit %s: %v", filePath, verr), tooldef.WithError()), nil
		}
	}

	content := string(data)
	updated, err := applyEdit(content, oldString, newString, replaceAll)
	if err != nil {
		return tooldef.NewToolResult(err.Error(), tooldef.WithError()), nil
	}

	if err := writeFileAtomic(filePath, []byte(updated)); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("cannot write file: %v", err), tooldef.WithError()), nil
	}
	if t.tracker != nil {
		t.tracker.Record(filePath, []byte(updated))
	}

	diff := DiffFiles(input.FilePath, data, []byte(updated))
	return tooldef.NewToolResult("Edit applied. "+diff.Summary(), tooldef.WithMetadata(diffMetadata(diff))), nil
}

// applyEdit performs the string replacement, returning the error the model
// sees when old_string is missing or ambiguous. Shared with the approval
// preview so both compute the same result.
func applyEdit(content, oldString, newString string, replaceAll bool) (string, error) {
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", errors.New("old_string not found")
	}
	if !replaceAll && count > 1 {
		return "", fmt.Errorf("old_string not unique: %d occurrences", count)
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString), nil
	}
	return strings.Replace(content, oldString, newString, 1), nil
}
