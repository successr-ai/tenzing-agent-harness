package systemone

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// maxMessageChars truncates one message in the tail. A judgment needs the
// shape of the conversation, not a pasted file — and a state that outgrows the
// question is the documented way to lose accuracy.
const maxMessageChars = 2000

// turnState is what batch A judges: what the user asked for, and what the
// agent has been saying about it. Tool calls and their results stay out —
// whether an agent is committing, reversing, stuck or declaring done shows in
// its own words, and a tail of `[called Edit]` lines crowds those words out.
// The consult count stays: it is the one fact about tools the advisor
// question turns on ("it has just consulted").
type turnState struct {
	Iteration         int      `json:"iteration"`
	ElapsedSeconds    int      `json:"elapsed_seconds"`
	AdvisorConsults   int      `json:"advisor_consults_this_turn"`
	Request           string   `json:"request,omitempty"`
	AssistantMessages []string `json:"recent_assistant_messages,omitempty"`
}

// TurnState builds batch A's state from its parts, for evals/ — the same
// struct the harness sends, so a fixture cannot drift from the live shape.
// Nothing else should call it.
func TurnState(iteration, elapsedSeconds, consults int, request string, assistant []string) any {
	return turnState{
		Iteration:         iteration,
		ElapsedSeconds:    elapsedSeconds,
		AdvisorConsults:   consults,
		Request:           request,
		AssistantMessages: assistant,
	}
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
	// Paths are the locations the call resolves to, placed relative to the
	// working directory and the system temp directory. Computed, not judged.
	Paths []pathFact `json:"paths,omitempty"`
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
	out := make([]string, 0, len(msgs)+1)
	for _, m := range msgs {
		if text := renderMessage(m); text != "" {
			out = append(out, text)
		}
	}
	return e.withRequest(out, n)
}

// assistantMessages is batch A's tail: the text of the last RecentMessages
// assistant messages, newest last. A message that only called tools says
// nothing in words and is skipped, so the N slots go to what the agent said.
func (e *Ext) assistantMessages(ctx context.Context) []string {
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
	var out []string
	for i := len(msgs) - 1; i >= 0 && len(out) < n; i-- {
		if msgs[i].Role != common.RoleAssistant {
			continue
		}
		if text := assistantText(msgs[i]); text != "" {
			out = append(out, text)
		}
	}
	slices.Reverse(out)
	return out
}

// assistantText is a message's prose: text blocks only, truncated.
func assistantText(m common.Message) string {
	var parts []string
	for _, block := range m.Content {
		if block.Type == common.ContentTypeText && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return truncate(strings.Join(parts, " "), maxMessageChars)
}

// requestText is the pinned request without its role prefix.
func (e *Ext) requestText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.TrimPrefix(e.request, string(common.RoleUser)+": ")
}

// pinRequest records the turn's opening user message — the last user message
// in the store when iteration 1 begins, since the loop appends it before the
// first hooks run. Every state the turn sends carries it: batch A as its
// `request` field, batch B pinned at the head of the message tail.
func (e *Ext) pinRequest(ctx context.Context) {
	e.mu.Lock()
	src := e.messages
	e.mu.Unlock()
	var request string
	if src != nil {
		if msgs, err := src(ctx); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == common.RoleUser {
					request = renderMessage(msgs[i])
					break
				}
			}
		}
	}
	e.mu.Lock()
	e.request = request
	e.mu.Unlock()
}

// withRequest keeps the turn's request at the head of the tail. A rolling
// window of the last N messages loses the request as soon as the agent has
// made a few calls — observed live on a retry: the tail held two tool calls,
// a result and a denial, and no trace of what the user had asked for, so the
// question "is this outside what the user asked for?" was judged against
// nothing. The request takes one slot; the tail keeps the newest N-1.
func (e *Ext) withRequest(tail []string, n int) []string {
	e.mu.Lock()
	request := e.request
	e.mu.Unlock()
	if request == "" {
		return tail
	}
	for _, line := range tail {
		if line == request {
			return tail
		}
	}
	if len(tail) >= n && n > 1 {
		tail = tail[len(tail)-(n-1):]
	}
	return append([]string{request}, tail...)
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
