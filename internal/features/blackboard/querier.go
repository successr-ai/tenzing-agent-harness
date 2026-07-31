package blackboard

import (
	"context"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Querier answers single-shot LLM prompts. Implementations must be safe for
// concurrent use: llm_batch fans out up to 8 Query calls at once.
type Querier interface {
	Query(ctx context.Context, prompt string, maxTokens int64) (string, error)
}

type llmQuerier struct {
	llm common.LLM
}

func NewLLMQuerier(llm common.LLM) Querier {
	return &llmQuerier{llm: llm}
}

func (q *llmQuerier) Query(ctx context.Context, prompt string, maxTokens int64) (string, error) {
	// Plumbing call: disable model reasoning — llm_query blocks the shared
	// REPL, so latency matters more than deliberation.
	noThink := false
	resp, err := q.llm.SendSyncMessage(ctx, common.CompletionRequest{
		Model:     q.llm.GetCurrentModel(),
		System:    "Answer concisely and accurately.",
		Messages:  []common.Message{common.NewUserMessage(prompt)},
		MaxTokens: maxTokens,
		Think:     &noThink,
	})
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}
