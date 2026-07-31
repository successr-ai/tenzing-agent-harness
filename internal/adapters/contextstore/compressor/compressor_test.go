package compressor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const testContextWindow = 10_000 // yields threshold of 30_000 chars (10000 * compressAtFraction)
var testThreshold = testContextWindow * compressAtFraction

type fakeLLM struct {
	response string
	err      error
	lastReq  common.CompletionRequest
}

func (f *fakeLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return common.CompletionResponse{}, f.err
	}
	return common.CompletionResponse{
		Content: []common.ContentBlock{common.NewTextContent(f.response)},
	}, nil
}

func (f *fakeLLM) SendStreamingMessage(context.Context, common.CompletionRequest, chan<- common.StreamEvent) error {
	return common.ErrNotSupported
}

func (f *fakeLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, common.ErrNotSupported
}

func (f *fakeLLM) CountTokens(context.Context, common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, common.ErrNotSupported
}

func (f *fakeLLM) ListModels(context.Context) ([]common.ModelInfo, error) {
	return nil, common.ErrNotSupported
}

func (f *fakeLLM) GetCurrentModel() string   { return "fake-model" }
func (f *fakeLLM) GetContextWindowSize() int { return 128_000 }

func (f *fakeLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "fake-model", ContextWindowSize: 128000, SupportsVision: true}
}

func newTestCompressor(t *testing.T, llm common.LLM, contextWindow int) *Compressor {
	t.Helper()
	c := NewCompressor(llm, contextWindow)
	return c
}

func makeMessages(n int, charsPer int) []common.Message {
	msgs := make([]common.Message, n)
	text := strings.Repeat("x", charsPer)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = common.NewUserMessage(text)
		} else {
			msgs[i] = common.NewAssistantMessage(text)
		}
	}
	return msgs
}

func TestEstimateSize(t *testing.T) {
	tests := []struct {
		name     string
		messages []common.Message
		want     int
	}{
		{
			name:     "empty",
			messages: nil,
			want:     0,
		},
		{
			name:     "text blocks",
			messages: []common.Message{common.NewUserMessage("hello"), common.NewAssistantMessage("world")},
			want:     10,
		},
		{
			name: "tool result block weighted",
			messages: []common.Message{{
				Role: common.RoleTool,
				Content: []common.ContentBlock{
					common.NewToolResultContent("id-1", "tool", strings.Repeat("x", 40)),
				},
			}},
			want: 10, // 40 chars / toolOutputWeight(4) = 10
		},
		{
			name: "tool use block",
			messages: []common.Message{{
				Role: common.RoleAssistant,
				Content: []common.ContentBlock{
					common.NewToolUseContent("id-1", "bash", json.RawMessage(`{"cmd":"ls"}`)),
				},
			}},
			want: 12,
		},
	}

	c := NewCompressor(nil, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.EstimateSize(tt.messages)
			if got != tt.want {
				t.Errorf("EstimateSize = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMaybeCompressBelowThreshold(t *testing.T) {
	c := newTestCompressor(t, &fakeLLM{}, testContextWindow)

	msgs := makeMessages(10, 100)
	result, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if did {
		t.Fatal("should not compress below threshold")
	}
	if len(result) != len(msgs) {
		t.Fatalf("messages changed: got %d want %d", len(result), len(msgs))
	}
}

func TestMaybeCompressTooFewMessages(t *testing.T) {
	c := newTestCompressor(t, &fakeLLM{}, testContextWindow)

	msgs := makeMessages(KeepRecent, testThreshold/KeepRecent+1)
	result, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if did {
		t.Fatal("should not compress when <= KeepRecent messages")
	}
	if len(result) != len(msgs) {
		t.Fatalf("messages changed: got %d want %d", len(result), len(msgs))
	}
}

// Summarization is a plumbing call: it must explicitly disable model
// reasoning so thinking-by-default models don't burn tokens on it.
func TestSummarizeDisablesThinking(t *testing.T) {
	llm := &fakeLLM{response: "summary"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	msgs := makeMessages(totalMsgs, testThreshold/totalMsgs+1)
	if _, _, did, err := c.MaybeCompress(context.Background(), msgs); err != nil || !did {
		t.Fatalf("MaybeCompress did=%v err=%v, want compression", did, err)
	}
	if llm.lastReq.Think == nil || *llm.lastReq.Think {
		t.Errorf("summarize request Think = %v, want explicit false", llm.lastReq.Think)
	}
}

func TestMaybeCompressTriggered(t *testing.T) {
	llm := &fakeLLM{response: "summary of old conversation"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	charsPerMsg := testThreshold/totalMsgs + 1
	msgs := makeMessages(totalMsgs, charsPerMsg)

	result, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !did {
		t.Fatal("expected compression to trigger")
	}

	// 2 (summary + ack) + KeepRecent
	expectedLen := 2 + KeepRecent
	if len(result) != expectedLen {
		t.Fatalf("compressed len = %d, want %d", len(result), expectedLen)
	}

	if result[0].Role != common.RoleUser {
		t.Fatalf("first message role = %s, want user", result[0].Role)
	}
	if !strings.Contains(result[0].Content[0].Text, "summary of old conversation") {
		t.Fatal("summary not in first message")
	}
	if result[1].Role != common.RoleAssistant {
		t.Fatalf("second message role = %s, want assistant", result[1].Role)
	}
}

func TestMaybeCompressLLMError(t *testing.T) {
	llm := &fakeLLM{err: errors.New("api down")}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	charsPerMsg := testThreshold/totalMsgs + 1
	msgs := makeMessages(totalMsgs, charsPerMsg)

	result, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if did {
		t.Fatal("should not report compressed on error")
	}
	if len(result) != len(msgs) {
		t.Fatal("original messages should be returned on error")
	}
}

// The summarizer prompt must present the transcript as data with a mandated
// section skeleton — free-form prompts made glm continue the conversation in
// first person instead of summarizing.
func TestSummarizePromptShape(t *testing.T) {
	llm := &fakeLLM{response: "summary"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	msgs := makeMessages(totalMsgs, testThreshold/totalMsgs+1)
	if _, _, did, err := c.MaybeCompress(context.Background(), msgs); err != nil || !did {
		t.Fatalf("MaybeCompress did=%v err=%v", did, err)
	}

	prompt := llm.lastReq.Messages[0].Content[0].Text
	for _, want := range []string{"<transcript>", "</transcript>", "## Decisions", "## Files touched", "## Current state", "## Open work"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if !strings.Contains(llm.lastReq.System, "third person") {
		t.Errorf("system prompt missing third-person instruction: %q", llm.lastReq.System)
	}
}

// The summary must end with a verbatim quote of where the agent stopped,
// appended deterministically in code (models misquote).
func TestSummaryAppendsLastPosition(t *testing.T) {
	llm := &fakeLLM{response: "## Decisions\nstuff"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	msgs := makeMessages(totalMsgs, testThreshold/totalMsgs+1)
	result, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err != nil || !did {
		t.Fatalf("MaybeCompress did=%v err=%v", did, err)
	}
	summary := result[0].Content[0].Text
	if !strings.Contains(summary, "## Last position") {
		t.Fatalf("summary missing Last position section:\n%s", summary)
	}
	// makeMessages alternates user/assistant; the last assistant message in
	// the compressed-away region must appear as a quoted tail.
	if !strings.Contains(summary, "> ") {
		t.Fatalf("Last position missing quoted tail:\n%s", summary)
	}
}

// Over-budget transcripts keep head and tail with an explicit omission
// marker — the old flat 20k cap silently dropped ~93% of long histories.
func TestSummarizeInputBudgetHeadTail(t *testing.T) {
	llm := &fakeLLM{response: "summary"}
	c := newTestCompressor(t, llm, testContextWindow) // budget = 10_000*4/2 = 20_000 chars

	msgs := []common.Message{
		common.NewUserMessage("HEADMARK" + strings.Repeat("a", 40_000) + "TAILMARK"),
	}
	for len(msgs) <= KeepRecent {
		msgs = append(msgs, common.NewUserMessage(strings.Repeat("b", 100)))
	}
	if _, _, did, err := c.MaybeCompress(context.Background(), msgs); err != nil || !did {
		t.Fatalf("MaybeCompress did=%v err=%v", did, err)
	}
	prompt := llm.lastReq.Messages[0].Content[0].Text
	if !strings.Contains(prompt, "HEADMARK") {
		t.Error("head of transcript missing")
	}
	if !strings.Contains(prompt, "TAILMARK") {
		t.Error("tail of transcript missing")
	}
	if !strings.Contains(prompt, "chars omitted") {
		t.Error("omission marker missing")
	}
}

func TestSummarizeInputCappedAtBudget(t *testing.T) {
	llm := &fakeLLM{response: "truncated summary"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 2
	msgs := makeMessages(totalMsgs, c.summarizeBudget)

	_, _, did, err := c.MaybeCompress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !did {
		t.Fatal("expected compression")
	}

	inputText := llm.lastReq.Messages[0].Content[0].Text
	if len(inputText) > c.summarizeBudget+500 {
		t.Fatalf("input not capped: len=%d budget=%d", len(inputText), c.summarizeBudget)
	}
}

func TestCompressPreservesRecentMessages(t *testing.T) {
	llm := &fakeLLM{response: "summary"}
	c := newTestCompressor(t, llm, testContextWindow)

	totalMsgs := KeepRecent + 4
	charsPerMsg := testThreshold/totalMsgs + 1
	msgs := makeMessages(totalMsgs, charsPerMsg)

	// Tag the last KeepRecent messages so we can verify they're preserved
	for i := totalMsgs - KeepRecent; i < totalMsgs; i++ {
		tag := strings.Repeat("y", charsPerMsg)
		if i%2 == 0 {
			msgs[i] = common.NewUserMessage(tag)
		} else {
			msgs[i] = common.NewAssistantMessage(tag)
		}
	}

	result, _, _, _ := c.MaybeCompress(context.Background(), msgs)

	// Skip summary pair (indices 0,1), check rest match recent
	for i := 2; i < len(result); i++ {
		got := result[i].Content[0].Text
		want := msgs[totalMsgs-KeepRecent+(i-2)].Content[0].Text
		if got != want {
			t.Fatalf("recent message %d not preserved", i-2)
		}
	}
}
