package systemone

import (
	"context"
	"log/slog"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// maxMessageChars truncates one message in the tail. A judgment needs the
// shape of the conversation, not a pasted file — and a state that outgrows the
// question is the documented way to lose accuracy.
const maxMessageChars = 2000

// turnState is what batch A judges: where the turn is, and what the agent has
// been doing in it.
type turnState struct {
	Iteration       int      `json:"iteration"`
	ElapsedSeconds  int      `json:"elapsed_seconds"`
	RecentTools     []string `json:"recent_tools,omitempty"`
	AdvisorConsults int      `json:"advisor_consults_this_turn"`
	RecentMessages  []string `json:"recent_messages,omitempty"`
}

// callState is one pending tool call as batch B sees it.
type callState struct {
	ToolCall       toolCall `json:"tool_call"`
	RecentMessages []string `json:"recent_messages,omitempty"`
}

type toolCall struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Input  string `json:"input"`
	// Decision is what the harness already decided by policy, so the model
	// judges the call rather than re-deriving the permission rules.
	Decision string `json:"harness_decision"`
	ReadOnly bool   `json:"read_only"`
}

func decisionName(d core.Decision) string {
	switch d {
	case core.Deny:
		return "deny"
	case core.AskUser:
		return "ask the user"
	default:
		return "allow"
	}
}

// recentMessages renders the trailing conversation as plain text lines. A
// failing or unset source yields nothing: the batch still runs, on a smaller
// state.
func (e *Ext) recentMessages(ctx context.Context) []string {
	e.mu.Lock()
	src, n := e.messages, e.recentN
	e.mu.Unlock()
	if src == nil || n <= 0 {
		return nil
	}
	msgs, err := src(ctx)
	if err != nil {
		slog.Warn("system one: conversation unavailable; judging without it",
			"ext", e.Name(), "error", err)
		return nil
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if text := renderMessage(m); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// renderMessage flattens one message to "<role>: <text>". Thinking blocks are
// dropped (chain-of-thought is not what the questions ask about) and images
// cannot travel — a System One state is text only.
func renderMessage(m common.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		switch block.Type {
		case common.ContentTypeText:
			b.WriteString(block.Text)
		case common.ContentTypeToolUse:
			b.WriteString("[called " + block.ToolName + "]")
		case common.ContentTypeToolResult:
			b.WriteString("[result of " + block.ToolName + ": " + block.ToolOutput + "]")
		case common.ContentTypeImage:
			b.WriteString("[image]")
		}
		b.WriteString(" ")
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return ""
	}
	return string(m.Role) + ": " + truncate(text, maxMessageChars)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}
