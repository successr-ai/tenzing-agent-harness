package builtins

import "github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"

// Defaults returns the standard builtin tool set. Each call constructs a
// fresh FileTracker shared by Read/Edit/Write, enforcing read-before-edit
// per registry — subagents get fresh registries, so isolation is automatic.
func Defaults() []tooldef.Definition {
	tracker := NewFileTracker()
	return []tooldef.Definition{
		&BashTool{},
		NewReadTool(tracker),
		NewEditTool(tracker),
		NewWriteTool(tracker),
		&GrepTool{},
		&GlobTool{},
		&LsTool{},
	}
}
