package contextstore

import (
	"context"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// recordingLLM is a minimal common.LLM stub that counts SendSyncMessage
// calls (the compressor's summarize call) and reports a tiny context
// window so the compression threshold is trivially exceeded.
type recordingLLM struct {
	calls int
}

func (r *recordingLLM) SendSyncMessage(context.Context, common.CompletionRequest) (common.CompletionResponse, error) {
	r.calls++
	return common.CompletionResponse{Content: []common.ContentBlock{common.NewTextContent("summary")}}, nil
}
func (r *recordingLLM) SendStreamingMessage(context.Context, common.CompletionRequest, chan<- common.StreamEvent) error {
	return nil
}
func (r *recordingLLM) SendMessageWithTools(context.Context, common.CompletionRequest, []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (r *recordingLLM) CountTokens(context.Context, common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (r *recordingLLM) ListModels(context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (r *recordingLLM) GetCurrentModel() string                                { return "stub" }
func (r *recordingLLM) GetContextWindowSize() int                              { return 1 }

func (r *recordingLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "recording-model", ContextWindowSize: 128000, SupportsVision: true}
}

func TestPairsToolResultsWithPendingToolUses(t *testing.T) {
	s := New(Config{})
	ctx := context.Background()
	if err := s.AppendUser(ctx, "do a thing"); err != nil {
		t.Fatal(err)
	}
	assistant := common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewToolUseContent("tu_1", "bash", []byte(`{"cmd":"ls"}`)),
		common.NewToolUseContent("tu_2", "read", []byte(`{"path":"x"}`)),
	}}
	if err := s.AppendAssistant(ctx, assistant); err != nil {
		t.Fatal(err)
	}
	err := s.AppendToolResults(ctx, []core.ToolResult{
		{ToolUseID: "tu_1", Output: "file1"},
		{ToolUseID: "tu_2", Output: "contents"},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != common.RoleTool || len(last.Content) != 2 {
		t.Fatalf("tool results not paired: %+v", last)
	}
}

func TestMissingResultGetsPlaceholder(t *testing.T) {
	s := New(Config{})
	ctx := context.Background()
	_ = s.AppendAssistant(ctx, common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewToolUseContent("tu_1", "bash", nil),
		common.NewToolUseContent("tu_2", "bash", nil),
	}})
	_ = s.AppendToolResults(ctx, []core.ToolResult{{ToolUseID: "tu_1", Output: "ok"}})
	msgs, _ := s.Messages(ctx)
	last := msgs[len(msgs)-1]
	if len(last.Content) != 2 {
		t.Fatalf("unanswered tool_use must get a placeholder result (Anthropic API rejects orphans): %+v", last)
	}
}

// TestNoCompressionAfterToolResultsAppend guards against re-checking
// compression on every append. The source (internal/adapters/contextstore/store.go)
// only ran the check when the last appended message was
// RoleAssistant; a compaction firing mid-turn after a user/tool-result
// append (with the store's mutex held across a synchronous LLM call) is a
// regression this test catches.
func TestNoCompressionAfterToolResultsAppend(t *testing.T) {
	llm := &recordingLLM{}
	s := New(Config{LLM: llm})
	ctx := context.Background()

	big := strings.Repeat("x", 200)

	// Push the message count and byte-size well past the compressor's
	// threshold using only AppendUser + AppendToolResults (never
	// AppendAssistant) — history never ends on a RoleAssistant message here,
	// so the compressor's summarize LLM must never be called.
	for i := 0; i < 7; i++ {
		if err := s.AppendUser(ctx, big); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendToolResults(ctx, []core.ToolResult{{ToolUseID: "tu_1", Output: big}}); err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 {
		t.Fatalf("compressor invoked after a non-assistant append: calls=%d", llm.calls)
	}

	// An assistant append crossing the same (already-exceeded) threshold
	// must trigger compression.
	if err := s.AppendAssistant(ctx, common.Message{Role: common.RoleAssistant, Content: []common.ContentBlock{
		common.NewTextContent(big),
	}}); err != nil {
		t.Fatal(err)
	}
	if llm.calls == 0 {
		t.Fatal("expected compressor to be invoked after an assistant append crossing threshold")
	}
}

func TestInitialMemorySeedsHistory(t *testing.T) {
	s := New(Config{InitialMemory: "previous session summary"})
	msgs, _ := s.Messages(context.Background())
	if len(msgs) == 0 {
		t.Fatal("initial memory must seed the history")
	}
}
