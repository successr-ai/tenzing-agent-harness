package builtins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
)

// Regression: a timed-out command whose pipeline children outlive the shell
// must not block Execute. Killing only the shell left orphaned children
// holding the output pipe, so CombinedOutput blocked far past the deadline
// and froze the whole agent loop.
func TestBashToolTimeoutKillsPipelineChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tool := &BashTool{}
	start := time.Now()
	res, err := tool.Execute(ctx, tooldef.ExecutionContext{
		WorkingDir: t.TempDir(),
		Arguments:  []string{`{"command":"sleep 15 | sleep 15"}`},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Execute blocked %v waiting on orphaned pipeline children, want < 3s", elapsed)
	}
	if !strings.Contains(res.Output, "[timed out]") {
		t.Fatalf("output missing [timed out] marker: %q", res.Output)
	}
}
