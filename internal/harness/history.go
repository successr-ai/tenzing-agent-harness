package harness

import (
	"fmt"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// SetHistory replaces the conversation with caller-supplied messages, e.g. a
// transcript held by an external chat UI, so the next RunTurn continues it.
// The history must be shaped the way the harness itself records one (see
// validateHistory). The conversation ID is unchanged and the injected
// messages are not written to the session record — only turns run after
// this are. Stateless callers should pair it with WithSessionDisabled.
// Only between turns.
func (h *Harness) SetHistory(msgs []common.Message) error {
	if !h.idle() {
		return errBusy
	}
	if err := validateHistory(msgs); err != nil {
		return fmt.Errorf("invalid history: %w", err)
	}
	h.mainStore.Replace(msgs)
	return nil
}

// validateHistory enforces the shape every provider accepts: no system
// messages (the system prompt is the harness's), user first, assistant last
// (the next turn appends the user message), roles alternating between
// assistant and user/tool, and each assistant tool_use answered by a tool
// result in the RoleTool message right after it.
func validateHistory(msgs []common.Message) error {
	var pending map[string]bool // tool_use ids awaiting results
	for i, m := range msgs {
		if len(m.Content) == 0 {
			return fmt.Errorf("message %d: empty content", i)
		}
		wantAssistant := i%2 == 1
		switch m.Role {
		case common.RoleAssistant:
			if !wantAssistant {
				return fmt.Errorf("message %d: assistant where user expected", i)
			}
			pending = toolUseIDs(m)
		case common.RoleUser, common.RoleTool:
			if wantAssistant {
				return fmt.Errorf("message %d: %s where assistant expected", i, m.Role)
			}
			if err := checkToolResults(i, m, pending); err != nil {
				return err
			}
			pending = nil
		default:
			return fmt.Errorf("message %d: role %q not allowed", i, m.Role)
		}
	}
	if len(msgs) > 0 && msgs[len(msgs)-1].Role != common.RoleAssistant {
		return fmt.Errorf("last message must be from the assistant")
	}
	if len(pending) > 0 {
		return fmt.Errorf("last message: tool calls have no results")
	}
	return nil
}

func toolUseIDs(m common.Message) map[string]bool {
	ids := map[string]bool{}
	for _, b := range m.Content {
		if b.Type == common.ContentTypeToolUse {
			ids[b.ToolUseID] = true
		}
	}
	return ids
}

// checkToolResults requires a RoleTool message to answer exactly the
// preceding assistant's tool_use ids, and a RoleUser message to follow an
// assistant that made no tool calls.
func checkToolResults(i int, m common.Message, pending map[string]bool) error {
	if m.Role == common.RoleUser {
		if len(pending) > 0 {
			return fmt.Errorf("message %d: tool calls before it have no results", i)
		}
		return nil
	}
	answered := map[string]bool{}
	for _, b := range m.Content {
		if b.Type != common.ContentTypeToolResult || !pending[b.ToolResultID] {
			return fmt.Errorf("message %d: tool message holds a block that answers no pending tool call", i)
		}
		answered[b.ToolResultID] = true
	}
	if len(answered) != len(pending) {
		return fmt.Errorf("message %d: %d of %d tool calls answered", i, len(answered), len(pending))
	}
	return nil
}
