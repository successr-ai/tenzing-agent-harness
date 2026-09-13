// Package tenzing is the public API for running agents programmatically.
// It re-exports the harness constructor, options, and the supporting types
// needed to use them from outside this module. Build an LLM client from a
// protocol package (pkg/providers/protocols/...) and a model (pkg/models or
// any Model implementation), create a harness with New, then run a single
// loop with Harness.RunTurn.
//
//	llm, err := anthropic.NewClient(models.Anthropic_ClaudeSonnet4_6, anthropic.WithAPIKey(key))
//	if err != nil { ... }
//	h, err := tenzing.New(llm, tenzing.WithSystemPrompt("..."))
//	if err != nil { ... }
//	defer h.Shutdown()
//	answer, err := h.RunTurn(ctx, "do the thing")
package tenzing

import (
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/runner"
)

// Harness wires an agent, tool registry, and event bus into a runnable loop.
type Harness = harness.Harness

// Option configures a Harness at construction time.
type Option = harness.HarnessOption

// New constructs a Harness around the given main LLM client. See the With*
// options for configuration.
var New = harness.New

// Harness options.
var (
	// WithAgentBuilder overrides how the Agent ("brain") is built from an
	// LLM client and system prompt; applies to the main agent and subagents.
	WithAgentBuilder = harness.WithAgentBuilder

	// WithSubagentLLM sets the client used for spawned subagents. Unset
	// falls back to the main LLM.
	WithSubagentLLM = harness.WithSubagentLLM

	// WithSubagentDepth sets the maximum subagent nesting depth. 0 disables
	// the spawn_agent tool entirely.
	WithSubagentDepth = harness.WithSubagentDepth

	// WithSubagentMaxIterations caps a spawned subagent's loop iterations.
	WithSubagentMaxIterations = harness.WithSubagentMaxIterations

	// WithBlackboardLLM sets the client used for llm_query/llm_batch calls
	// inside the shared blackboard REPL; unset falls back to the main LLM.
	// These are stateless one-shot completions (no tools, no agent loop) —
	// not subagents — so a small/fast model is often the right choice.
	WithBlackboardLLM = harness.WithBlackboardLLM

	// WithAdvisorLLM enables the transcript-aware advisor tool and its
	// write-gate using the given client. The advisor automatically sees the
	// main conversation; the gate denies each turn's first state-changing
	// tool call until the advisor has been consulted. Without this option
	// neither is registered.
	WithAdvisorLLM = harness.WithAdvisorLLM

	// WithAdvisorNudge reminds an executor that has not consulted the
	// advisor yet, from the given loop iteration onward (0 disables, the
	// default). Only meaningful together with WithAdvisorLLM.
	WithAdvisorNudge = harness.WithAdvisorNudge

	// WithAdvisorExemptTools exempts the named tools from the advisor
	// write-gate: they run even as a turn's first, unconsulted,
	// state-changing call. Use for a harness whose only state-changing
	// action is a forced/schema-only answer tool with no orientation phase
	// to precede it — gating it would deny the call and force a
	// deny→advisor→retry round-trip the harness has no budget for. Ignored
	// unless WithAdvisorLLM is also set.
	WithAdvisorExemptTools = harness.WithAdvisorExemptTools

	// WithDisabledTool removes a tool by name (case-insensitive) after all
	// registration, including built-ins like "bash" and "edit".
	WithDisabledTool = harness.WithDisabledTool

	// WithSkillsDir registers an additional skills directory. Nonexistent
	// or unreadable directories are skipped at discovery time.
	WithSkillsDir = harness.WithSkillsDir

	// WithPluginsDir registers an additional Claude config directory (the
	// one holding settings.json and plugins/) whose enabled plugins
	// contribute skills, namespaced "<plugin>:<skill>". Nonexistent or
	// unreadable directories are skipped at discovery time.
	WithPluginsDir = harness.WithPluginsDir

	// WithTool registers a custom tool definition; first registration of a
	// name wins.
	WithTool = harness.WithTool

	// WithHooks subscribes typed event callbacks to the harness event bus.
	WithHooks = harness.WithHooks

	// WithSystemPrompt replaces the default main-agent system prompt.
	WithSystemPrompt = harness.WithSystemPrompt

	// WithConversationID resumes a prior conversation: the main agent runs
	// under this ID and its latest memory file is loaded as initial
	// context. The caller owns ID uniqueness across live processes.
	WithConversationID = harness.WithConversationID

	// WithEventBus replaces the harness's event bus with a caller-owned one.
	WithEventBus = harness.WithEventBus

	// WithTextDeltaHandler registers a callback for incremental text
	// output, tagged with the emitting runner's ID.
	WithTextDeltaHandler = harness.WithTextDeltaHandler

	// WithThinkingDeltaHandler registers a callback for incremental
	// thinking output, tagged with the emitting runner's ID.
	WithThinkingDeltaHandler = harness.WithThinkingDeltaHandler

	// WithExtension registers an additional core extension. Order of
	// WithExtension calls is hook execution order.
	WithExtension = harness.WithExtension

	// WithPermissionPolicy replaces the default tool permission policy
	// (DefaultPermissionPolicy: ask for code-executing/file-writing tools,
	// allow the rest).
	WithPermissionPolicy = harness.WithPermissionPolicy

	// WithPermissionsDisabled skips the permissions extension entirely —
	// every tool call runs unquestioned. Explicit opt-out for headless or
	// fully trusted drivers.
	WithPermissionsDisabled = harness.WithPermissionsDisabled

	// WithDangerouslySkipPermissions auto-approves every AskUser escalation
	// in the main loop — no approval request is emitted and nothing blocks
	// waiting for an answer. For sandboxed environments (Docker containers,
	// CI pipelines) where no human can respond to approval prompts.
	//
	// Difference from WithPermissionsDisabled: that option removes the
	// permissions extension itself, so the default policy never escalates —
	// but any other hook (a caller-supplied extension via WithExtension)
	// can still escalate to AskUser, and unattended those escalations are
	// denied. This option instead leaves every hook in place and flips the
	// outcome: whatever escalates to AskUser is approved. Deny decisions
	// (policy denylist, read-only mode, tool-call gates) still deny — this
	// skips the asking, not the blocking. The two compose independently.
	WithDangerouslySkipPermissions = harness.WithDangerouslySkipPermissions

	// WithReadOnly denies every tool call whose tool is not marked
	// read-only — main agent and all subagents. Replaces the permissions
	// extension; denials are instant errors, never approval prompts.
	WithReadOnly = harness.WithReadOnly

	// WithApprovalTimeout bounds how long an AskUser tool call waits for an
	// approval response before being denied. Default 120s; 0 denies
	// immediately (unattended drivers with nobody to answer).
	WithApprovalTimeout = harness.WithApprovalTimeout

	// WithSessionDir relocates message-level session persistence (default
	// <UserConfigDir>/tenzing/sessions).
	WithSessionDir = harness.WithSessionDir

	// WithSessionDisabled turns off message-level session persistence; the
	// compression-summary memory files remain the only resume mechanism.
	WithSessionDisabled = harness.WithSessionDisabled

	// WithPromptTemplatesDir registers an additional directory of *.md
	// slash-command templates, invoked as "/name args..." in a RunTurn
	// query.
	WithPromptTemplatesDir = harness.WithPromptTemplatesDir

	// WithContextFilesDisabled turns off automatic AGENTS.md loading into
	// the main system prompt.
	WithContextFilesDisabled = harness.WithContextFilesDisabled

	// WithToolCallGate installs a gate consulted before every tool call —
	// the main agent's and all subagents'. Returning a non-nil error blocks
	// the call; the error string is fed back to the model as the tool
	// result so it can adapt.
	WithToolCallGate = harness.WithToolCallGate

	// WithThinking toggles model reasoning for the main agent's requests.
	// Without this option the provider default applies.
	WithThinking = harness.WithThinking

	// WithThinkingBudget caps reasoning tokens per LLM call for the main
	// agent (exact on Anthropic, tiered/level-mapped elsewhere).
	WithThinkingBudget = harness.WithThinkingBudget

	// WithLLMRetry tunes the default agent's transient-LLM-error retry
	// policy: max attempts (negative disables) and the base backoff delay.
	WithLLMRetry = harness.WithLLMRetry

	// WithCompressionThreshold overrides the auto-compress trigger point as
	// a fraction of the model's context window (default 0.75).
	WithCompressionThreshold = harness.WithCompressionThreshold

	// WithCompressionKeepMessages overrides how many recent messages
	// survive auto-compression uncompressed (default 6).
	WithCompressionKeepMessages = harness.WithCompressionKeepMessages
)

// ToolCallGate is the pre-execution veto installed via WithToolCallGate.
type ToolCallGate = harness.ToolCallGate

// ReadOnlyReporter is the optional marker interface a custom tool
// implements (ReadOnly() bool → true) to run concurrently with adjacent
// read-only calls in a tool batch; unmarked tools act as barriers.
type ReadOnlyReporter = tooldef.ReadOnlyReporter

// ErrVisionUnsupported is returned by Harness.RunTurnWithImages when the
// current model does not accept image input (checked before any API call).
var ErrVisionUnsupported = harness.ErrVisionUnsupported

// Tool permission policy (default-on: code-executing/file-writing tools ask
// for approval; opt out with WithPermissionsDisabled).
type PermissionPolicy = permissions.Policy

// DefaultPermissionPolicy returns the built-in policy: code-executing and
// file-writing tools escalate to AskUser, everything else is allowed.
var DefaultPermissionPolicy = permissions.DefaultPolicy

// Budget limits for WithBudgets; zero fields are unlimited.
type BudgetLimits = budgets.Limits

// WithBudgets registers the budgets extension for the main loop: the turn
// terminates gracefully when any limit is exceeded. Zero fields are
// unlimited.
var WithBudgets = harness.WithBudgets

// MCP server config for WithMCPServer (stdio transport).
type MCPServerConfig = mcp.ServerConfig

// WithMCPServer mounts an external MCP server (stdio transport) as a
// dynamic tool source.
var WithMCPServer = harness.WithMCPServer

// Extension system: implement Extension plus any of the capability hook
// interfaces below and register via WithExtension.
type (
	Extension = core.Extension

	SessionStartHook    = core.SessionStartHook
	SessionEndHook      = core.SessionEndHook
	BeforeIterationHook = core.BeforeIterationHook
	ToolCallHook        = core.ToolCallHook
	ToolResultHook      = core.ToolResultHook
	AfterTurnHook       = core.AfterTurnHook
	PromptContributor   = core.PromptContributor
	ToolProvider        = core.ToolProvider
	DynamicToolSource   = core.DynamicToolSource

	TurnContext       = core.TurnContext
	ToolCallContext   = core.ToolCallContext
	ToolResultContext = core.ToolResultContext
	TurnResult        = core.TurnResult
	ToolSpec          = core.ToolSpec
	Decision          = core.Decision
)

// Tool-gating decisions for ToolCallHook implementations.
const (
	Allow   = core.Allow
	AskUser = core.AskUser
	Deny    = core.Deny
)

// Agent is the "brain" contract consumed by the runner; implement it and
// pass a builder via WithAgentBuilder to replace the default agent.
type (
	Agent           = core.Agent
	AgentBuilder    = runner.AgentBuilder
	ReasoningResult = core.ReasoningResult
)

// Tool types, for implementing custom tools passed via WithTool.
type (
	ToolDefinition   = tooldef.Definition
	ToolResult       = core.ToolResult
	ToolResultOption = tooldef.ToolResultOption
	ToolCall         = core.ToolCall
	ExecutionContext = tooldef.ExecutionContext
	Schema           = tooldef.Schema
	SchemaProperty   = tooldef.SchemaProperty
	JsonType         = tooldef.JsonType
)

// Tool result constructors.
var (
	NewToolResult       = tooldef.NewToolResult
	WithToolUseID       = tooldef.WithToolUseID
	WithToolMetadata    = tooldef.WithMetadata
	WithToolResultError = tooldef.WithError
)

// JSON schema type names for tool schemas.
const (
	JsonTypeObject  = tooldef.JsonTypeObject
	JsonTypeString  = tooldef.JsonTypeString
	JsonTypeNumber  = tooldef.JsonTypeNumber
	JsonTypeInteger = tooldef.JsonTypeInteger
	JsonTypeBoolean = tooldef.JsonTypeBoolean
	JsonTypeArray   = tooldef.JsonTypeArray
)

// Event system, for WithEventBus / WithHooks consumers.
type (
	Event    = core.Event
	EventBus = eventbus.EventBus
	Hooks    = eventbus.Hooks
)

// NewEventBus creates a standalone event bus for WithEventBus.
var NewEventBus = eventbus.NewEventBus

// Typed events delivered to Hooks callbacks and EventBus subscribers.
type (
	SessionStartedEvent        = core.SessionStartedEvent
	SessionEndedEvent          = core.SessionEndedEvent
	TurnStartedEvent           = core.TurnStartedEvent
	TurnCompletedEvent         = core.TurnCompletedEvent
	LoopStartedEvent           = core.LoopStartedEvent
	LoopStoppedEvent           = core.LoopStoppedEvent
	ReasoningStartedEvent      = core.ReasoningStartedEvent
	ReasoningFinishedEvent     = core.ReasoningFinishedEvent
	ToolExecutionStartedEvent  = core.ToolExecutionStartedEvent
	ToolExecutionFinishedEvent = core.ToolExecutionFinishedEvent
	LLMResponseEvent           = core.LLMResponseEvent
	ToolSucceededEvent         = core.ToolSucceededEvent
	ToolFailedEvent            = core.ToolFailedEvent
	ToolDeniedEvent            = core.ToolDeniedEvent
	ToolProgressEvent          = core.ToolProgressEvent
	ContextCompressingEvent    = core.ContextCompressingEvent
	ContextCompressedEvent     = core.ContextCompressedEvent
	ErrorEvent                 = core.ErrorEvent
	SubagentStartedEvent       = core.SubagentStartedEvent
	SubagentStoppedEvent       = core.SubagentStoppedEvent
	TaskCreatedEvent           = core.TaskCreatedEvent
	TaskCompletedEvent         = core.TaskCompletedEvent
	ApprovalRequestedEvent     = core.ApprovalRequestedEvent
	SteeringInjectedEvent      = core.SteeringInjectedEvent
	LLMRetryEvent              = core.LLMRetryEvent
	ModelChangedEvent          = core.ModelChangedEvent
	ThinkingChangedEvent       = core.ThinkingChangedEvent
	ImagesAttachedEvent        = core.ImagesAttachedEvent
	ImageData                  = core.ImageData
)
