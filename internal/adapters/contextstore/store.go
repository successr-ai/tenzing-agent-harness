// Package contextstore provides an in-memory adapter for core.ContextPort.
// It owns the conversation history: user/assistant messages, tool_use ->
// tool_result pairing (required by the Anthropic API, which rejects a
// tool_use left unanswered in the immediately following message), and
// compression when the history grows past the compressor's threshold.
package contextstore

import (
	"context"
	"log/slog"
	"sync"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore/compressor"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// placeholderOutput is used for tool_use ids that never received a matching
// result — the Anthropic API rejects histories where a tool_use has no
// tool_result in the immediately following message.
const placeholderOutput = "tool call was not executed"

// Config configures a Store.
type Config struct {
	LLM common.LLM // for compression summaries; nil disables compression
	// InitialMemory is a prior session's summary, injected as the first
	// exchange. Empty means a fresh conversation.
	InitialMemory string
	// InitialHistory seeds the store with a full reconstructed message
	// history (session resume); takes precedence over InitialMemory.
	InitialHistory []common.Message
	Emitter        core.Emitter // ContextCompressedEvent on compaction; nil = no events
	RunnerID       string
	// CompressionThreshold / CompressionKeepMessages tune auto-compression;
	// zero keeps the defaults (0.75 of context window / 6 messages).
	CompressionThreshold    float64
	CompressionKeepMessages int
}

// Store is an in-memory, mutex-guarded core.ContextPort implementation.
type Store struct {
	mu      sync.Mutex
	msgs    []common.Message
	pending []common.ContentBlock // tool_use blocks awaiting results
	comp    *compressor.Compressor
	emitter core.Emitter
	id      string
}

var _ core.ContextPort = (*Store)(nil)

// New constructs a Store. When cfg.LLM is nil, compression is disabled.
func New(cfg Config) *Store {
	s := &Store{
		msgs:    make([]common.Message, 0),
		emitter: cfg.Emitter,
		id:      cfg.RunnerID,
	}

	if cfg.LLM != nil {
		s.comp = compressor.NewCompressor(cfg.LLM, cfg.LLM.GetContextWindowSize())
		if cfg.CompressionThreshold > 0 {
			s.comp.SetThresholdFraction(cfg.CompressionThreshold)
		}
		if cfg.CompressionKeepMessages > 0 {
			s.comp.SetKeepRecent(cfg.CompressionKeepMessages)
		}
	}

	switch {
	case len(cfg.InitialHistory) > 0:
		s.msgs = append(s.msgs, cfg.InitialHistory...)
	case cfg.InitialMemory != "":
		s.msgs = append(s.msgs,
			common.NewUserMessage("[Context summary from previous conversation]\n\n"+cfg.InitialMemory),
			common.NewAssistantMessage("Understood. I have the full context from our previous work."),
		)
	}

	return s
}

// Messages returns a copy of the current history; callers never see
// internal slices.
func (s *Store) Messages(ctx context.Context) ([]common.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]common.Message, len(s.msgs))
	copy(out, s.msgs)
	return out, nil
}

// AppendUser appends a user message and runs the compression check.
func (s *Store) AppendUser(ctx context.Context, text string) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, common.NewUserMessage(text))
	s.mu.Unlock()

	s.maybeCompress(ctx)
	return nil
}

// AppendUserContent appends one user message with arbitrary content blocks
// (text + images) and runs the compression check.
func (s *Store) AppendUserContent(ctx context.Context, blocks []common.ContentBlock) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, common.Message{Role: common.RoleUser, Content: blocks})
	s.mu.Unlock()

	s.maybeCompress(ctx)
	return nil
}

// AppendAssistant appends an assistant message, extracts any tool_use
// blocks into s.pending for the next AppendToolResults call to pair against,
// and runs the compression check.
func (s *Store) AppendAssistant(ctx context.Context, msg common.Message) error {
	resp := common.CompletionResponse{Content: msg.Content}
	toolCalls := resp.ToolCalls()

	s.mu.Lock()
	s.msgs = append(s.msgs, msg)
	s.pending = toolCalls
	s.mu.Unlock()

	s.maybeCompress(ctx)
	return nil
}

// AppendToolResults pairs results to the pending tool_use blocks by
// ToolUseID and appends one RoleTool message. Pending ids with no matching
// result get a placeholder so the model never sees an orphaned tool_use.
func (s *Store) AppendToolResults(ctx context.Context, results []core.ToolResult) error {
	byID := make(map[string]core.ToolResult, len(results))
	for _, r := range results {
		byID[r.ToolUseID] = r
	}

	s.mu.Lock()
	pendingIDs := make(map[string]struct{}, len(s.pending))
	blocks := make([]common.ContentBlock, 0, len(s.pending))
	for _, tu := range s.pending {
		pendingIDs[tu.ToolUseID] = struct{}{}
		output := placeholderOutput
		if r, ok := byID[tu.ToolUseID]; ok {
			output = r.Output
		}
		blocks = append(blocks, common.NewToolResultContent(tu.ToolUseID, tu.ToolName, output))
	}
	s.pending = nil
	// RoleTool: every provider converter renders this natively — the
	// Anthropic converter as a user message with tool_result blocks,
	// Ollama/OpenAI as role-"tool" messages. A plain RoleUser message would
	// drop the blocks in the Ollama/OpenAI text conversion.
	s.msgs = append(s.msgs, common.Message{Role: common.RoleTool, Content: blocks})
	s.mu.Unlock()

	// A result whose ToolUseID has no matching pending tool_use is silently
	// dropped above (only s.pending drives block construction) — log it so
	// the drop isn't invisible.
	for _, r := range results {
		if _, ok := pendingIDs[r.ToolUseID]; !ok {
			slog.Debug("tool result has no matching pending tool_use; dropped", "tool_use_id", r.ToolUseID)
		}
	}

	s.maybeCompress(ctx)
	return nil
}

// Clear drops the conversation history entirely. Only call between turns:
// clearing mid-turn would strand tool_use blocks that the loop is still
// collecting results for.
func (s *Store) Clear() {
	s.Replace(nil)
}

// Replace swaps the history wholesale — session resume loading a persisted
// conversation over the live one. Pending tool_use blocks go with it: they
// belonged to the history being replaced, and the incoming one is already
// self-consistent.
func (s *Store) Replace(msgs []common.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = make([]common.Message, len(msgs))
	copy(s.msgs, msgs)
	s.pending = nil
}

// SetLLM swaps the compression-summary client (mid-session model
// switching). Only call between turns. No-op when compression is disabled.
func (s *Store) SetLLM(llm common.LLM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.comp != nil {
		s.comp.SetLLM(llm)
	}
}

// Compact forces context compression regardless of thresholds, optionally
// steering the summary. Returns false when there was nothing to compact
// (short history or compression disabled). Only call between turns.
func (s *Store) Compact(ctx context.Context, instructions string) (bool, error) {
	if s.comp == nil {
		return false, nil
	}

	s.mu.Lock()
	before := len(s.msgs)
	compressed, summary, did, err := s.comp.Compress(ctx, s.msgs, instructions)
	if err != nil || !did {
		s.mu.Unlock()
		return false, err
	}
	s.msgs = compressed
	after := len(s.msgs)
	s.mu.Unlock()

	if s.emitter != nil {
		s.emitter.Emit(core.ContextCompressedEvent{
			BaseEvent:      core.NewBaseEvent(core.EventContextCompressed, s.id),
			MessagesBefore: before,
			MessagesAfter:  after,
			Summary:        summary,
		})
	}
	return true, nil
}

// maybeCompress runs the compressor's threshold check and, if compaction
// happens, replaces the in-memory history and emits ContextCompressedEvent.
func (s *Store) maybeCompress(ctx context.Context) {
	if s.comp == nil {
		return
	}

	s.mu.Lock()
	if len(s.msgs) == 0 || s.msgs[len(s.msgs)-1].Role != common.RoleAssistant {
		s.mu.Unlock()
		return
	}
	before := len(s.msgs)
	compressed, summary, did, err := s.comp.MaybeCompress(ctx, s.msgs)
	if err != nil {
		s.mu.Unlock()
		return
	}
	if !did {
		s.mu.Unlock()
		return
	}
	s.msgs = compressed
	after := len(s.msgs)
	s.mu.Unlock()

	if s.emitter != nil {
		s.emitter.Emit(core.ContextCompressedEvent{
			BaseEvent:      core.NewBaseEvent(core.EventContextCompressed, s.id),
			MessagesBefore: before,
			MessagesAfter:  after,
			Summary:        summary,
		})
	}
}
