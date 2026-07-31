package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/prompts"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// AgentRunner is a thin facade over core.Loop. It owns construction-time
// wiring (tool definitions, skill map, stream callbacks) and translates
// TurnResult into the (string, error) contract the harness expects.
type AgentRunner struct {
	id           string
	agent        core.Agent
	loop         *core.Loop
	systemPrompt string
}

type agentRunnerOptions struct {
	id              string
	toolRegistry    *toolport.Registry
	toolPort        core.ToolPort
	contextStore    core.ContextPort
	onTextDelta     func(runnerID, text string)
	onThinkingDelta func(runnerID, text string)
	systemPrompt    string
	emitter         core.Emitter
	extensions      *core.Extensions
	approvalTimeout time.Duration
	skipPermissions bool
}

type AgentRunnerOption func(*agentRunnerOptions)

func WithEmitter(emitter core.Emitter) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.emitter = emitter
	}
}

// WithID assigns a pre-generated runner ID (see NewID) instead of a random
// one. Used when the ID must be known before the runner exists — e.g.
// hierarchical sub-agent IDs that double as blackboard slot names.
func WithID(id string) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		if id != "" {
			o.id = id
		}
	}
}

// WithTextDeltaHandler registers a streaming-text callback. The runner
// tags each delta with its own id before invoking f, so consumers can
// correlate deltas across concurrent runners.
func WithTextDeltaHandler(f func(runnerID, text string)) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.onTextDelta = f
	}
}

// WithThinkingDeltaHandler registers a streaming-thinking callback, tagged
// with the runner id like WithTextDeltaHandler.
func WithThinkingDeltaHandler(f func(runnerID, text string)) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.onThinkingDelta = f
	}
}

func WithSystemPrompt(prompt string) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		if prompt != "" {
			o.systemPrompt = prompt
		}
	}
}

func WithToolRegistry(registry *toolport.Registry) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.toolRegistry = registry
	}
}

// WithToolPort supplies the ToolPort the loop drives (e.g. the composite).
// When unset, the runner wraps the tool registry into a plain port.
func WithToolPort(port core.ToolPort) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.toolPort = port
	}
}

// WithContextStore configures the ContextPort that owns this runner's
// conversation history. Required — the runner rebuilds messages from it
// every iteration and appends the assistant/tool_result turns back to it.
func WithContextStore(cs core.ContextPort) AgentRunnerOption {
	return func(o *agentRunnerOptions) {
		o.contextStore = cs
	}
}

func WithExtensions(exts *core.Extensions) AgentRunnerOption {
	return func(o *agentRunnerOptions) { o.extensions = exts }
}

// WithApprovalTimeout bounds how long an AskUser tool call blocks awaiting
// an ApprovalRequestedEvent response. 0 (the default) denies immediately.
func WithApprovalTimeout(d time.Duration) AgentRunnerOption {
	return func(o *agentRunnerOptions) { o.approvalTimeout = d }
}

// WithSkipPermissions auto-approves every AskUser tool call instead of
// emitting an ApprovalRequestedEvent. Deny decisions still deny.
func WithSkipPermissions() AgentRunnerOption {
	return func(o *agentRunnerOptions) { o.skipPermissions = true }
}

// NewAgentRunner creates a new AgentRunner. It performs one-time wiring
// (tool definitions, skill map, stream callbacks on the Agent) and builds
// a core.Loop for RunLoop to delegate to.
func NewAgentRunner(agent core.Agent, opts ...AgentRunnerOption) (*AgentRunner, error) {
	if agent == nil {
		return nil, fmt.Errorf("no agent defined")
	}

	o := &agentRunnerOptions{
		systemPrompt: prompts.DefaultSystemPrompt(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.toolPort == nil && o.toolRegistry == nil {
		return nil, fmt.Errorf("no tool registry defined")
	}
	if o.contextStore == nil {
		return nil, fmt.Errorf("no context store defined")
	}
	if o.id == "" {
		o.id = NewID()
	}
	if o.extensions == nil {
		o.extensions = core.NewExtensions()
	}

	// One-time agent wiring — these are construction-time setups, not
	// per-iteration calls. The core.Loop never touches them. The agent's
	// callback contract is func(text); the runner closes over its own id
	// so deltas stay correlatable across concurrent runners.
	if o.onTextDelta != nil {
		f, id := o.onTextDelta, o.id
		agent.UpdateStreamCallback(func(text string) { f(id, text) })
	}
	if o.onThinkingDelta != nil {
		f, id := o.onThinkingDelta, o.id
		agent.UpdateThinkingCallback(func(text string) { f(id, text) })
	}

	tp := o.toolPort
	if tp == nil {
		tp = toolport.Wrap(o.toolRegistry)
	}

	loop, err := core.NewLoop(core.LoopConfig{
		ID:              o.id,
		Model:           agent,
		Tools:           tp,
		Context:         o.contextStore,
		Emitter:         o.emitter,
		Extensions:      o.extensions,
		SystemPrompt:    o.systemPrompt,
		ApprovalTimeout: o.approvalTimeout,
		SkipPermissions: o.skipPermissions,
	})
	if err != nil {
		return nil, fmt.Errorf("create loop: %w", err)
	}

	return &AgentRunner{
		id:           o.id,
		agent:        agent,
		loop:         loop,
		systemPrompt: o.systemPrompt,
	}, nil
}

// NewID returns a fresh 8-hex-char runner ID (e.g. "438314ea"). Exported so
// callers can pre-generate an ID and pass it via WithID.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *AgentRunner) GetCurrentModel() string {
	return h.agent.GetCurrentModel()
}

// RunLoop executes a single turn: user input -> agent plan/execute loop -> agent result.
// It delegates to core.Loop.RunTurn and translates TurnResult into the
// (string, error) contract the harness expects.
func (h *AgentRunner) RunLoop(ctx context.Context, input string) (string, error) {
	return h.translate(h.loop.RunTurn(ctx, input))
}

// RunLoopWithImages is RunLoop with image content blocks attached to the
// turn's user message. The caller owns the vision-capability check.
func (h *AgentRunner) RunLoopWithImages(ctx context.Context, input string, images []common.ImageSource) (string, error) {
	return h.translate(h.loop.RunTurnWithImages(ctx, input, images))
}

func (h *AgentRunner) translate(tr core.TurnResult, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if tr.Terminated != "" {
		return tr.FinalAnswer, fmt.Errorf("terminated: %s", tr.Terminated)
	}
	return tr.FinalAnswer, nil
}

func (h *AgentRunner) ID() string {
	return h.id
}

// Steer queues a user message for injection into the running loop at the
// next tool-execution boundary. Safe for concurrent use.
func (h *AgentRunner) Steer(msg string) error {
	return h.loop.Steer(msg)
}

// LoopState reports the loop FSM's current state (e.g. "started",
// "stopped", "reasoning_started"). Safe for concurrent use.
func (h *AgentRunner) LoopState() string {
	return h.loop.State()
}

func (h *AgentRunner) SystemPrompt() string {
	return h.systemPrompt
}
