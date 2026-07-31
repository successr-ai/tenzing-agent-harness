package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

const (
	maxGrepMatches = 500
)

var _ tooldef.Definition = (*GrepTool)(nil)

type GrepTool struct{}

func (t *GrepTool) Name() string { return "Grep" }

// ReadOnly marks Grep as safe for concurrent execution within a tool batch.
func (t *GrepTool) ReadOnly() bool { return true }

func (t *GrepTool) Description() string {
	return "Search files for a regexp pattern, returning file:line:content matches."
}

func (t *GrepTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"pattern": {Type: tooldef.JsonTypeString},
			"path":    {Type: tooldef.JsonTypeString},
			"include": {Type: tooldef.JsonTypeString},
		},
		Required: []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) == 0 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("pattern is required", tooldef.WithError()), nil
	}

	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Pattern == "" {
		return tooldef.NewToolResult("pattern is required", tooldef.WithError()), nil
	}

	re, err := regexp.Compile(input.Pattern)
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid regexp: %v", err), tooldef.WithError()), nil
	}

	searchRoot := exctx.WorkingDir
	if input.Path != "" {
		searchRoot = input.Path
	}
	if searchRoot == "" {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return core.ToolResult{}, fmt.Errorf("unable to get cwd: %w", wdErr)
		}
		searchRoot = wd
	}

	includePattern := input.Include

	var matches []string
	capped := false
	err = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if includePattern != "" {
			matched, matchErr := filepath.Match(includePattern, filepath.Base(path))
			if matchErr != nil || !matched {
				return nil
			}
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if isBinary(data) {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", path, i+1, line))
				if len(matches) >= maxGrepMatches {
					capped = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return tooldef.NewToolResult(fmt.Sprintf("walk error: %v", err), tooldef.WithError()), nil
	}

	if len(matches) == 0 {
		return tooldef.NewToolResult("No matches."), nil
	}
	output := strings.Join(matches, "\n")
	if capped {
		output += fmt.Sprintf("\n[truncated at %d matches]", maxGrepMatches)
	}
	return tooldef.NewToolResult(output), nil
}

func isBinary(data []byte) bool {
	probe := data
	if len(probe) > 1024 {
		probe = probe[:1024]
	}
	return bytes.IndexByte(probe, 0) != -1
}
