// Package advisor consults a second, stronger reasoning model for strategic
// guidance mid-task. Modeled on Anthropic's server-side advisor tool: the
// advisor automatically sees the executor's full conversation transcript (the
// model passes at most an optional question), implemented client-side so it
// works with every provider. The advisor model should be at least as capable
// as the executor model; this is not enforced.
//
// The package also provides GateExt (ext.go), a write-gate extension that
// denies the first state-changing tool call of a turn until the advisor has
// been consulted. Both activate together via harness.WithAdvisorLLM.
package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// maxTokensStdResponse caps advisor output per call. 2048 matches Anthropic's
// recommended advisor cap (~7x output reduction vs uncapped, no measured
// quality loss); the system prompt additionally asks for ~80-word answers.
const maxTokensStdResponse int64 = 2048

// transcript rendering limits.
const (
	maxRenderedResultChars = 500  // per tool result / tool input excerpt
	responseHeadroomTokens = 4096 // reserved for system prompt + advisor reply
	charsPerToken          = 4    // rough estimate for budget math
)

var _ tooldef.Definition = (*AdvisorTool)(nil)

const systemPrompt = "You are a senior technical advisor. You see the executor agent's full " +
	"conversation transcript: the task, every tool call and result, and its reasoning so far. " +
	"Identify risks, wrong assumptions, missing steps, and simpler alternatives. " +
	"If the approach is sound, say so briefly rather than inventing objections. " +
	"Keep your guidance under roughly 80 words unless a critical risk demands more."

// HistoryFunc returns the executor conversation to show the advisor.
type HistoryFunc func(ctx context.Context) ([]common.Message, error)

type AdvisorTool struct {
	llm     common.LLM
	history HistoryFunc
}

// NewAdvisorTool builds the advisor tool. history supplies the executor's
// conversation at call time (nil renders an empty transcript).
func NewAdvisorTool(llm common.LLM, history HistoryFunc) *AdvisorTool {
	return &AdvisorTool{llm: llm, history: history}
}

func (t *AdvisorTool) Name() string { return "advisor" }

func (t *AdvisorTool) Description() string {
	return "Consult a stronger reviewer model that automatically sees your full " +
		"conversation — the task, every tool call and result. No arguments needed. " +
		"Call before substantive work (writing, editing, committing to an approach), " +
		"when stuck, or before declaring the task done. Orientation reads first are fine. " +
		"Optionally pass a specific question."
}

// ReadOnly marks advisor as mutation-free (a one-shot LLM consultation):
// safe for concurrent batch execution and allowed in read-only mode.
func (t *AdvisorTool) ReadOnly() bool { return true }

func (t *AdvisorTool) Schema() tooldef.Schema {
	return tooldef.Schema{
		Properties: map[string]tooldef.SchemaProperty{
			"question": {Type: tooldef.JsonTypeString},
		},
	}
}

func (t *AdvisorTool) Execute(ctx context.Context, exctx tooldef.ExecutionContext) (core.ToolResult, error) {
	var input struct {
		Question string `json:"question"`
	}
	if len(exctx.Arguments) > 0 && exctx.Arguments[0] != "" {
		if err := json.Unmarshal([]byte(exctx.Arguments[0]), &input); err != nil {
			return tooldef.NewToolResult(fmt.Sprintf("invalid input JSON: %v", err), tooldef.WithError()), nil
		}
	}

	var msgs []common.Message
	if t.history != nil {
		var err error
		msgs, err = t.history(ctx)
		if err != nil {
			return tooldef.NewToolResult(fmt.Sprintf("advisor error: reading conversation: %v", err), tooldef.WithError()), nil
		}
	}

	// Reserve headroom for the system prompt and reply, but never let the
	// transcript budget fall below half the window (small-context models).
	window := t.llm.GetContextWindowSize()
	budget := max(window-responseHeadroomTokens, window/2) * charsPerToken
	transcript := renderTranscript(msgs, budget)

	var prompt strings.Builder
	prompt.WriteString("Executor conversation transcript:\n\n")
	prompt.WriteString(transcript)
	if input.Question != "" {
		prompt.WriteString("\n\nThe executor's specific question:\n")
		prompt.WriteString(input.Question)
	}

	// Reasoning off: maxTokensStdResponse is too small to fund both a
	// thinking pass and the answer, and a busy thinking pass can eat the
	// whole budget and leave the response text empty.
	noThink := false
	resp, err := t.llm.SendSyncMessage(ctx, common.CompletionRequest{
		Model:     t.llm.GetCurrentModel(),
		System:    systemPrompt,
		Messages:  []common.Message{common.NewUserMessage(prompt.String())},
		MaxTokens: maxTokensStdResponse,
		Think:     &noThink,
	})
	if err != nil {
		return tooldef.NewToolResult(fmt.Sprintf("advisor error: %v", err), tooldef.WithError()), nil
	}

	slog.Debug("advisor consulted",
		"model", t.llm.GetCurrentModel(),
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)

	return tooldef.NewToolResult(resp.Text()), nil
}

// renderTranscript renders messages to text, truncating oldest-first so the
// result stays within charBudget. Thinking blocks are omitted; long tool
// inputs/results are excerpted.
func renderTranscript(msgs []common.Message, charBudget int) string {
	if len(msgs) == 0 {
		return "(no conversation yet)"
	}

	rendered := make([]string, len(msgs))
	for i, m := range msgs {
		rendered[i] = renderMessage(m)
	}

	// Walk newest→oldest, keeping whole messages while they fit.
	total := 0
	start := len(rendered)
	for i := len(rendered) - 1; i >= 0; i-- {
		total += len(rendered[i]) + 1
		if total > charBudget && start < len(rendered) {
			break
		}
		start = i
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString("[earlier conversation truncated]\n\n")
	}
	for _, r := range rendered[start:] {
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

func renderMessage(m common.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		switch block.Type {
		case common.ContentTypeText:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			fmt.Fprintf(&b, "[%s]\n%s\n", m.Role, block.Text)
		case common.ContentTypeToolUse:
			fmt.Fprintf(&b, "[%s tool call] %s(%s)\n", m.Role, block.ToolName, excerpt(string(block.ToolInput)))
		case common.ContentTypeToolResult:
			fmt.Fprintf(&b, "[tool result: %s]\n%s\n", block.ToolName, excerpt(block.ToolOutput))
		case common.ContentTypeImage:
			fmt.Fprintf(&b, "[%s attached an image]\n", m.Role)
		}
		// thinking blocks omitted: advice should react to actions, not drafts
	}
	return b.String()
}

func excerpt(s string) string {
	if len(s) <= maxRenderedResultChars {
		return s
	}
	return s[:maxRenderedResultChars] + "… [truncated]"
}
