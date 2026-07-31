package agent

import (
	"context"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// captureLLM records every CompletionRequest and answers with fixed text
// (plus usage, when set).
type captureLLM struct {
	requests []common.CompletionRequest
	usage    common.Usage
}

func (c *captureLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (c *captureLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, ch chan<- common.StreamEvent) error {
	close(ch)
	return nil
}
func (c *captureLLM) SendMessageWithTools(_ context.Context, req common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	c.requests = append(c.requests, req)
	return common.CompletionResponse{
		Model:   "capture",
		Content: []common.ContentBlock{common.NewTextContent("ok")},
		Usage:   c.usage,
	}, nil
}
func (c *captureLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (c *captureLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (c *captureLLM) GetCurrentModel() string                                  { return "capture" }
func (c *captureLLM) GetContextWindowSize() int                                { return 4096 }

func (c *captureLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "capture-model", ContextWindowSize: 128000, SupportsVision: true}
}

// Every request asks the provider to cache system prompt + tools, and the
// provider's cache token counts surface in the response meta.
func TestDoReasoningPromptCaching(t *testing.T) {
	llm := &captureLLM{usage: common.Usage{
		InputTokens:              100,
		OutputTokens:             10,
		CacheReadInputTokens:     55,
		CacheCreationInputTokens: 44,
	}}
	a, err := New(AgentConfig{Model: llm, SystemPrompt: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := a.DoReasoning(context.Background(), userMsgs("hi"), nil, nil)
	if err != nil {
		t.Fatalf("DoReasoning: %v", err)
	}

	if len(llm.requests) != 1 || !llm.requests[0].CacheSystemAndTools {
		t.Error("CacheSystemAndTools not set on the request")
	}
	if result.Meta.CacheReadInputTokens != 55 || result.Meta.CacheCreationInputTokens != 44 {
		t.Errorf("cache tokens not surfaced in meta: %+v", result.Meta)
	}
}
