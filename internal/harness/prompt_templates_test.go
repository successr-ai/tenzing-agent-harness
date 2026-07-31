package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
)

// Templates registered via WithPromptTemplatesDir expand "/name args..."
// queries before the agent loop sees them.
func TestRunTurnExpandsPromptTemplate(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "review.md", "---\ndescription: Review a file\n---\nReview $1 for ${2:-bugs}.")

	scripted := newScriptedAgent(finalStep("ok"))
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPromptTemplatesDir(dir),
	)
	t.Cleanup(h.Shutdown)

	if _, err := h.RunTurn(context.Background(), "/review src/foo.go"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	calls := scripted.capturedCalls()
	if len(calls) == 0 {
		t.Fatal("agent never called")
	}
	var joined strings.Builder
	for _, m := range calls[0].Messages {
		joined.WriteString(common.CombinedText(m.Content))
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "Review src/foo.go for bugs.") {
		t.Errorf("agent input not expanded: %q", joined.String())
	}
}

// An unknown "/name" fails fast with the available names — no LLM turn.
func TestRunTurnUnknownTemplateErrors(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "review.md", "body")

	scripted := newScriptedAgent(finalStep("ok"))
	h := newTestHarness(t,
		WithAgentBuilder(func(common.LLM, string) (core.Agent, error) { return scripted, nil }),
		WithPromptTemplatesDir(dir),
	)
	t.Cleanup(h.Shutdown)

	_, err := h.RunTurn(context.Background(), "/deploy prod")
	if err == nil || !strings.Contains(err.Error(), "/review") {
		t.Fatalf("expected unknown-template error listing /review, got %v", err)
	}
	assertCallCount(t, scripted, 0)
}

// Without WithPromptTemplatesDir, plain queries pass through untouched.
func TestRunTurnNoTemplatesPassthrough(t *testing.T) {
	h := newTestHarness(t)
	if _, err := h.RunTurn(context.Background(), "hello there"); err != nil {
		t.Fatalf("plain query: %v", err)
	}
}
