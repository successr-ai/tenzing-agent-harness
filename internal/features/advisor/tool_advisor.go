// Package advisor provides a tool that consults a second, stronger reasoning
// model for strategic guidance on a plan before the main agent commits to it.
// Modeled on Anthropic's server-side advisor tool (executor model generates,
// advisor model is consulted for planning) but implemented client-side so it
// works with every provider. The advisor model should be at least as capable
// as the executor model; this is not enforced.
package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// maxTokensStdResponse caps output tokens per LLM request.
const maxTokensStdResponse int64 = 32768

var _ tooldef.Definition = (*AdvisorTool)(nil)

const systemPrompt = "You are a senior technical advisor reviewing a plan before execution. " +
	"Critique it: identify risks, missing steps, wrong assumptions, and simpler alternatives. " +
	"Be direct and concise. If the plan is sound, say so briefly rather than inventing objections."

type AdvisorTool struct {
	llm common.LLM
}

func NewAdvisorTool(llm common.LLM) *AdvisorTool {
	return &AdvisorTool{llm: llm}
}

func (t *AdvisorTool) Name() string { return "advisor" }

func (t *AdvisorTool) Description() string {
	return "Consult a stronger reasoning model for strategic guidance on a plan. " +
		"Use before committing to complex multi-step work, when stuck, or to " +
		"sanity-check an approach. Returns a critique: risks, missing steps, " +
		"and simpler alternatives. Provide the plan and any relevant task context."
}

// ReadOnly marks advisor as mutation-free (a one-shot LLM consultation):
// safe for concurrent batch execution and allowed in read-only mode.
func (t *AdvisorTool) ReadOnly() bool { return true }

func (t *AdvisorTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"plan":    {Type: tooldef.JsonTypeString},
			"context": {Type: tooldef.JsonTypeString},
		},
		Required: []string{"plan"},
	}
}

func (t *AdvisorTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	if len(exctx.Arguments) < 1 || exctx.Arguments[0] == "" {
		return tooldef.NewToolResult("plan is required", tooldef.WithError()), nil
	}

	var input struct {
		Plan    string `json:"plan"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
	}
	if input.Plan == "" {
		return tooldef.NewToolResult("plan is required", tooldef.WithError()), nil
	}

	var prompt strings.Builder
	if input.Context != "" {
		prompt.WriteString("Task context:\n")
		prompt.WriteString(input.Context)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Plan under review:\n")
	prompt.WriteString(input.Plan)

	resp, err := t.llm.SendSyncMessage(ctx, common.CompletionRequest{
		Model:     t.llm.GetCurrentModel(),
		System:    systemPrompt,
		Messages:  []common.Message{common.NewUserMessage(prompt.String())},
		MaxTokens: maxTokensStdResponse,
	})
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("advisor error: %v", err), tooldef.WithError()), nil
	}

	return tooldef.NewToolResult(resp.Text()), nil
}
