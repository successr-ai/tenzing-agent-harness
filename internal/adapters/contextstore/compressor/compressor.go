package compressor

import (
	"context"
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const (
	KeepRecent       = 6
	toolOutputWeight = 4
	// ~4 chars per token, compress at 75% of context window
	charsPerToken        = 4
	compressAtFraction   = 3      // numerator; denominator is charsPerToken (i.e. 3/4 = 75%)
	defaultContextWindow = 10_000 // tokens, fallback when caller doesn't specify
	// summarizer input budget = half the context window, in chars
	summarizeBudgetDivisor = 2
	// how much of the final pre-cut assistant message is quoted verbatim
	// under "## Last position"
	lastPositionTailChars = 500
	// imageEstimateChars is the size charged per image block when estimating
	// context usage: ~1600 tokens (a full-size provider image) at ~4 chars
	// per token. The base64 payload itself never reaches the summarizer.
	imageEstimateChars = 1600 * charsPerToken
)

const summarizeSystem = "You are a session summarizer. The user message contains a <transcript> of an agent session. " +
	"Write in third person, past tense. Never write in first person, never continue the conversation, never replay tool calls."

const summarizeInstruction = `Summarize the transcript into exactly these markdown sections, keeping all important decisions, code changes, and file paths:

## Decisions
## Files touched
## Current state
## Open work
`

type Compressor struct {
	llm             common.LLM
	contextWindow   int
	threshold       int
	summarizeBudget int
	keepRecent      int
}

// NewCompressor creates an in-context compressor. It performs no file I/O;
// persistence of summaries is the harness's job (it receives them via
// ContextCompressedEvent on the event bus).
func NewCompressor(llm common.LLM, contextWindow int) *Compressor {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}

	return &Compressor{
		llm:             llm,
		contextWindow:   contextWindow,
		threshold:       contextWindow * compressAtFraction,
		summarizeBudget: contextWindow * charsPerToken / summarizeBudgetDivisor,
		keepRecent:      KeepRecent,
	}
}

// SetLLM swaps the summarization client (mid-session model switching).
func (c *Compressor) SetLLM(llm common.LLM) {
	c.llm = llm
}

// SetThresholdFraction overrides the auto-compress trigger point (default
// 0.75 of the context window). Values outside (0,1] are ignored.
func (c *Compressor) SetThresholdFraction(frac float64) {
	if frac > 0 && frac <= 1 {
		c.threshold = int(float64(c.contextWindow*charsPerToken) * frac)
	}
}

// SetKeepRecent overrides how many recent messages survive compression
// verbatim (default 6). Non-positive values are ignored.
func (c *Compressor) SetKeepRecent(n int) {
	if n > 0 {
		c.keepRecent = n
	}
}

// MaybeCompress checks whether the message history exceeds the threshold.
// If so, it summarizes the older portion via LLM and returns a shorter
// history with the summary prepended, plus the summary text itself (the
// harness persists it; empty when no compression happened).
func (c *Compressor) MaybeCompress(ctx context.Context, messages []common.Message) ([]common.Message, string, bool, error) {
	if c.EstimateSize(messages) < c.threshold {
		return messages, "", false, nil
	}
	return c.Compress(ctx, messages, "")
}

// Compress summarizes unconditionally (manual compaction), bypassing the
// size threshold. instructions, when non-empty, steer the summary. Histories
// at or under the keep-recent tail are returned unchanged.
func (c *Compressor) Compress(ctx context.Context, messages []common.Message, instructions string) ([]common.Message, string, bool, error) {
	if len(messages) <= c.keepRecent {
		return messages, "", false, nil
	}

	splitIdx := len(messages) - c.keepRecent
	old := messages[:splitIdx]
	recent := messages[splitIdx:]

	summary, err := c.summarize(ctx, old, instructions)
	if err != nil {
		return messages, "", false, fmt.Errorf("compression summarize: %w", err)
	}

	compressed := make([]common.Message, 0, 2+len(recent))
	compressed = append(compressed,
		common.NewUserMessage("[Context summary from previous conversation]\n\n"+summary),
	)

	compressed = append(compressed,
		common.NewAssistantMessage("Understood. I have the full context from our previous work."),
	)
	compressed = append(compressed, recent...)

	return compressed, summary, true, nil
}

func (c *Compressor) Threshold() int { return c.threshold }

func (c *Compressor) EstimateSize(messages []common.Message) int {
	size := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			size += len(block.Text)
			size += len(block.ToolOutput) / toolOutputWeight
			size += len(block.ToolInput)
			if block.Image != nil {
				size += imageEstimateChars
			}
		}
	}
	return size
}

func (c *Compressor) summarize(ctx context.Context, messages []common.Message, instructions string) (string, error) {
	var sb strings.Builder
	for _, msg := range messages {
		fmt.Fprintf(&sb, "[%s] ", msg.Role)
		for _, block := range msg.Content {
			if block.Text != "" {
				sb.WriteString(block.Text)
			}
			if block.ToolOutput != "" {
				sb.WriteString(block.ToolOutput)
			}
		}
		sb.WriteString("\n")
	}

	text := sb.String()
	// Over budget: keep head and tail with an explicit gap marker so the
	// model knows content is missing (a flat cut silently dropped most of
	// long histories).
	if len(text) > c.summarizeBudget {
		head := text[:c.summarizeBudget*6/10]
		tail := text[len(text)-c.summarizeBudget*4/10:]
		omitted := len(text) - len(head) - len(tail)
		text = head + fmt.Sprintf("\n[... %d chars omitted ...]\n", omitted) + tail
	}

	// Plumbing call: disable model reasoning — summarization happens inside
	// the agent loop and shouldn't burn thinking tokens or latency.
	instruction := summarizeInstruction
	if instructions != "" {
		instruction += "\nAdditional instructions from the user for this summary:\n" + instructions + "\n"
	}

	noThink := false
	resp, err := c.llm.SendSyncMessage(ctx, common.CompletionRequest{
		Model:     c.llm.GetCurrentModel(),
		System:    summarizeSystem,
		Messages:  []common.Message{common.NewUserMessage(instruction + "\n<transcript>\n" + text + "\n</transcript>")},
		MaxTokens: 4096,
		Think:     &noThink,
	})
	if err != nil {
		return "", fmt.Errorf("summarize call: %w", err)
	}

	// Append where the agent stopped, deterministically — models misquote.
	summary := resp.Text() + "\n\n## Last position\n"
	if tail := lastAssistantTail(messages, lastPositionTailChars); tail != "" {
		summary += "> " + tail
	}
	return summary, nil
}

// lastAssistantTail returns the final n chars of the last assistant text in
// the compressed-away region, or "" when there is none.
func lastAssistantTail(messages []common.Message, n int) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != common.RoleAssistant {
			continue
		}
		text := common.CombinedText(messages[i].Content)
		if text == "" {
			continue
		}
		if len(text) > n {
			text = text[len(text)-n:]
		}
		return text
	}
	return ""
}
