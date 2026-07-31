package blackboard

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type fakeQuerierLLM struct {
	lastReq common.CompletionRequest
}

func (f *fakeQuerierLLM) SendSyncMessage(_ context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	f.lastReq = req
	return common.CompletionResponse{
		Content: []common.ContentBlock{common.NewTextContent("ok")},
	}, nil
}

func (f *fakeQuerierLLM) SendStreamingMessage(context.Context, common.CompletionRequest, chan<- common.StreamEvent) error {
	return common.ErrNotSupported
}

func (f *fakeQuerierLLM) SendMessageWithTools(context.Context, common.CompletionRequest, []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, common.ErrNotSupported
}

func (f *fakeQuerierLLM) CountTokens(context.Context, common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, common.ErrNotSupported
}

func (f *fakeQuerierLLM) ListModels(context.Context) ([]common.ModelInfo, error) {
	return nil, common.ErrNotSupported
}

func (f *fakeQuerierLLM) GetCurrentModel() string   { return "fake-model" }
func (f *fakeQuerierLLM) GetContextWindowSize() int { return 128_000 }

func (f *fakeQuerierLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "fake-model", ContextWindowSize: 128000, SupportsVision: true}
}

// llm_query blocks the shared REPL for every agent while it runs, so it must
// explicitly disable model reasoning rather than inherit a thinking default.
func TestLLMQuerierDisablesThinking(t *testing.T) {
	llm := &fakeQuerierLLM{}
	q := NewLLMQuerier(llm)

	got, err := q.Query(context.Background(), "prompt", 100)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got != "ok" {
		t.Errorf("Query = %q, want ok", got)
	}
	if llm.lastReq.Think == nil || *llm.lastReq.Think {
		t.Errorf("query request Think = %v, want explicit false", llm.lastReq.Think)
	}
}
