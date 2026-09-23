package harness

import (
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/core/tooldef"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/systemone"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/runner"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type harnessOptions struct {
	// agentBuilder builds the Agent ("brain") for the main loop and
	// subagents. Nil means the default agent implementation.
	agentBuilder runner.AgentBuilder

	// subagentLLM is the client for spawned subagents. Nil falls back to
	// the harness main LLM.
	subagentLLM common.LLM

	// subagentMaxDepth limits subagent nesting; 0 disables the spawn_agent
	// tool entirely.
	subagentMaxDepth int

	// subagentMaxIterations caps loop iterations per subagent.
	subagentMaxIterations int

	// blackboardLLM is the client used for llm_query/llm_batch sub-LM calls
	// inside the shared blackboard REPL; nil falls back to the main LLM.
	blackboardLLM common.LLM

	// advisorLLM enables the transcript-aware advisor tool and its write-gate
	// when non-nil.
	advisorLLM common.LLM

	// advisorNudge, when > 0, reminds an executor that has not consulted the
	// advisor from that iteration onward. 0 disables the nudge. Only
	// meaningful when advisorLLM is set.
	advisorNudge int

	// advisorCadence is the max loop iterations between advisor consults
	// before the gate blocks every tool. 0 = advisor.DefaultCadence; < 0
	// disables. Only meaningful when advisorLLM is set.
	advisorCadence int

	// advisorMaxCalls caps advisor consults per turn. 0 =
	// advisor.DefaultMaxCallsPerTurn. Only meaningful when advisorLLM is set.
	advisorMaxCalls int

	// advisorExemptTools names tools the advisor write-gate never blocks,
	// even unconsulted. Only meaningful when advisorLLM is set.
	advisorExemptTools []string

	// systemOne, when non-nil and carrying a client, registers the decision
	// model extension: tool gating, advisor-need judgment, model routing.
	systemOne *systemone.Config

	// resolveModel turns a model alias into a client, so a routing choice
	// can be applied. Nil leaves routing decided but never applied — the
	// harness cannot resolve aliases itself, that is the app layer's
	// registry.
	resolveModel func(alias string) (common.LLM, error)

	// onTextDelta is called with incremental text output from the agent,
	// tagged with the emitting runner's id. It is called from the agent's
	// goroutine, so it should not block.
	onTextDelta func(runnerID, text string)

	// onThinkingDelta is called with incremental thinking output from the
	// agent, tagged with the emitting runner's id. Called from the agent's
	// goroutine, so it should not block.
	onThinkingDelta func(runnerID, text string)

	// eventBus is an event bus that transports all events
	eventBus *eventbus.EventBus

	// mainSystemPrompt is the system prompt of the main agent
	mainSystemPrompt string

	// conversationID resumes a prior conversation: the main agent runs
	// under this ID and its latest memory file is loaded at startup.
	// Empty starts a fresh conversation under a random ID.
	conversationID string

	// hooks holds optional typed callbacks dispatched from the event
	// bus. Only set hooks fire; leave the rest nil.
	hooks eventbus.Hooks

	// extraTools are additional tools to add the registry
	extraTools map[string]tooldef.Definition

	// skillDirs are directories to find skills in
	skillDirs []string

	// pluginDirs are Claude config directories (holding settings.json and
	// plugins/) whose enabled plugins contribute namespaced skills.
	pluginDirs []string

	// disabledTools removes tools by name (case-insensitive) after all
	// registration, including built-ins like "bash" and "edit".
	disabledTools []string

	// extensions are additional core.Extension registrations, appended after
	// the default extensions (e.g. reminders). Order of WithExtension calls
	// is hook execution order.
	extensions []core.Extension

	// permissionPolicy overrides the default tool permission policy.
	// Nil means permissions.DefaultPolicy().
	permissionPolicy *permissions.Policy

	// permissionsDisabled skips registering the permissions extension
	// entirely (explicit opt-out for headless/trusted drivers).
	permissionsDisabled bool

	// dangerouslySkipPermissions auto-approves every AskUser decision in the
	// main loop instead of emitting an approval request.
	dangerouslySkipPermissions bool

	// approvalTimeout bounds how long an AskUser tool call waits for an
	// approval response before being denied.
	approvalTimeout time.Duration

	// budgetLimits, when non-zero, registers the budgets extension for the
	// main loop (graceful termination on iteration/wall-clock/token caps).
	budgetLimits budgets.Limits

	// mcpServers are external MCP servers to mount as dynamic tool sources.
	// The mcp extension is registered only when at least one is configured.
	mcpServers []mcp.ServerConfig

	// thinking toggles model reasoning for the main agent; nil leaves the
	// provider default.
	thinking *bool

	// thinkingBudget caps reasoning tokens per main-agent LLM call; nil
	// leaves the provider default.
	thinkingBudget *int64

	// llmRetryMax / llmRetryBaseDelay tune the default agent's transient-
	// error retry policy. Zero values keep the agent defaults (3 / 2s);
	// negative llmRetryMax disables retries.
	llmRetryMax       int
	llmRetryBaseDelay time.Duration

	// compressionThreshold / compressionKeepMessages tune the main context
	// store's auto-compression; zero keeps the defaults (0.75 / 6).
	compressionThreshold    float64
	compressionKeepMessages int

	// sessionDir relocates message-level session persistence (default
	// <UserConfigDir>/tenzing/sessions); sessionDisabled opts out entirely.
	sessionDir      string
	sessionDisabled bool

	// promptTemplateDirs are directories of *.md slash-command templates,
	// invoked as "/name args..." in a RunTurn query.
	promptTemplateDirs []string

	// contextFilesDisabled turns off automatic AGENTS.md loading into the
	// main system prompt.
	contextFilesDisabled bool

	// toolGate, when set, is consulted before every tool call (main agent
	// and all subagents).
	toolGate ToolCallGate

	// readOnly denies every tool call whose tool is not marked read-only and
	// forces approvalTimeout to 0 (no approval prompts ever fire).
	readOnly bool
}

func defaultHarnessOptions() *harnessOptions {
	return &harnessOptions{
		eventBus: eventbus.NewEventBus(),
		skillDirs: []string{
			"~/.claude/skills",
		},
		pluginDirs: []string{
			"~/.claude",
		},
		extraTools:            make(map[string]tooldef.Definition),
		subagentMaxDepth:      1,
		subagentMaxIterations: 100,
		approvalTimeout:       120 * time.Second,
	}
}

type HarnessOption func(*harnessOptions)

// WithAgentBuilder overrides how the Agent ("brain") is built from an LLM
// and system prompt. The default uses the built-in agent implementation.
func WithAgentBuilder(builder runner.AgentBuilder) HarnessOption {
	return func(o *harnessOptions) {
		o.agentBuilder = builder
	}
}

// WithSubagentLLM sets the client used for spawned subagents. Unset falls
// back to the main LLM.
func WithSubagentLLM(llm common.LLM) HarnessOption {
	return func(o *harnessOptions) {
		o.subagentLLM = llm
	}
}

// WithSubagentDepth sets the maximum subagent nesting depth. 0 disables the
// spawn_agent tool.
func WithSubagentDepth(depth int) HarnessOption {
	return func(o *harnessOptions) {
		o.subagentMaxDepth = depth
	}
}

func WithSubagentMaxIterations(maxIter int) HarnessOption {
	return func(o *harnessOptions) {
		o.subagentMaxIterations = maxIter
	}
}

// WithBlackboardLLM sets the client used for llm_query/llm_batch calls
// inside the shared blackboard REPL; unset falls back to the main LLM.
// These are stateless one-shot completions (no tools, no agent loop) —
// not subagents — so a small/fast model is often the right choice.
func WithBlackboardLLM(llm common.LLM) HarnessOption {
	return func(o *harnessOptions) {
		o.blackboardLLM = llm
	}
}

// WithAdvisorLLM enables the transcript-aware advisor tool and its write-gate
// using the given client. The advisor automatically sees the main
// conversation when called; the gate denies each turn's first state-changing
// tool call until the advisor has been consulted (read-only tools flow
// freely). Without this option neither is registered and behavior is
// unchanged.
func WithAdvisorLLM(llm common.LLM) HarnessOption {
	return func(o *harnessOptions) {
		o.advisorLLM = llm
	}
}

// WithAdvisorNudge reminds an executor that has not consulted the advisor
// yet, from the given loop iteration onward (0 disables, the default). Only
// meaningful together with WithAdvisorLLM; ignored otherwise. Off by default
// because nudging measurably helps weak executor models and hurts strong
// ones.
func WithAdvisorNudge(iteration int) HarnessOption {
	return func(o *harnessOptions) {
		o.advisorNudge = iteration
	}
}

// WithAdvisorCadence sets the max loop iterations the executor may run
// without consulting the advisor before the gate blocks every tool call
// (read-only included) until it does. 0 keeps advisor.DefaultCadence; a
// negative value disables the rule. Catches an executor oscillating between
// hypotheses on its own. Ignored unless WithAdvisorLLM is also set.
func WithAdvisorCadence(iterations int) HarnessOption {
	return func(o *harnessOptions) {
		o.advisorCadence = iterations
	}
}

// WithAdvisorMaxCalls caps advisor consults per turn; further calls are
// denied and the executor proceeds with the guidance it has. 0 keeps
// advisor.DefaultMaxCallsPerTurn. Ignored unless WithAdvisorLLM is also set.
func WithAdvisorMaxCalls(n int) HarnessOption {
	return func(o *harnessOptions) {
		o.advisorMaxCalls = n
	}
}

// WithAdvisorExemptTools exempts the named tools from the advisor write-gate:
// they run even as a turn's first, unconsulted, state-changing call. Use this
// for a harness whose only state-changing action is a forced/schema-only
// answer tool with no orientation phase to precede it — gating it would deny
// the call and force the executor into an extra deny→advisor→retry
// round-trip it has no budget for. Ignored unless WithAdvisorLLM is also set.
func WithAdvisorExemptTools(names ...string) HarnessOption {
	return func(o *harnessOptions) {
		o.advisorExemptTools = append(o.advisorExemptTools, names...)
	}
}

func WithDisabledTool(toolName string) HarnessOption {
	return func(o *harnessOptions) {
		o.disabledTools = append(o.disabledTools, toolName)
	}
}

// WithSkillsDir registers an additional skills directory. Nonexistent or
// unreadable directories are skipped at discovery time.
func WithSkillsDir(dir string) HarnessOption {
	return func(o *harnessOptions) {
		o.skillDirs = append(o.skillDirs, dir)
	}
}

// WithPluginsDir registers an additional Claude config directory (the one
// holding settings.json and plugins/) whose enabled plugins contribute
// skills, namespaced "<plugin>:<skill>". Nonexistent or unreadable
// directories are skipped at discovery time.
func WithPluginsDir(dir string) HarnessOption {
	return func(o *harnessOptions) {
		o.pluginDirs = append(o.pluginDirs, dir)
	}
}

func WithTool(tool tooldef.Definition) HarnessOption {
	return func(o *harnessOptions) {
		name := tool.Name()
		extraTools := o.extraTools
		if _, ok := extraTools[name]; !ok {
			extraTools[name] = tool
		}
	}
}

func WithHooks(hooks eventbus.Hooks) HarnessOption {
	return func(o *harnessOptions) {
		o.hooks = hooks
	}
}

func WithSystemPrompt(prompt string) HarnessOption {
	return func(o *harnessOptions) {
		o.mainSystemPrompt = prompt
	}
}

// WithConversationID resumes a prior conversation: the main agent runs under
// this ID and its latest memory file is loaded as initial context. The
// caller owns ID uniqueness across live processes.
func WithConversationID(id string) HarnessOption {
	return func(o *harnessOptions) {
		o.conversationID = id
	}
}

func WithEventBus(bus *eventbus.EventBus) HarnessOption {
	return func(o *harnessOptions) {
		o.eventBus = bus
	}
}

// WithTextDeltaHandler registers a callback for incremental text output.
// runnerID identifies the emitting runner so multiplexed consumers (RPC
// mode) can correlate deltas with their turn.
func WithTextDeltaHandler(f func(runnerID, text string)) HarnessOption {
	return func(o *harnessOptions) {
		o.onTextDelta = f
	}
}

// WithThinkingDeltaHandler registers a callback for incremental thinking
// output, tagged with the emitting runner's id like WithTextDeltaHandler.
func WithThinkingDeltaHandler(f func(runnerID, text string)) HarnessOption {
	return func(o *harnessOptions) {
		o.onThinkingDelta = f
	}
}

// WithExtension registers an additional core extension. Order of WithExtension
// calls is hook execution order (after the default extensions).
func WithExtension(ext core.Extension) HarnessOption {
	return func(o *harnessOptions) { o.extensions = append(o.extensions, ext) }
}

// WithPermissionPolicy replaces the default tool permission policy
// (permissions.DefaultPolicy: ask for code-executing/file-writing tools,
// allow the rest).
func WithPermissionPolicy(p permissions.Policy) HarnessOption {
	return func(o *harnessOptions) { o.permissionPolicy = &p }
}

// WithPermissionsDisabled skips the permissions extension entirely — every
// tool call runs unquestioned. Explicit opt-out for headless or fully
// trusted drivers.
func WithPermissionsDisabled() HarnessOption {
	return func(o *harnessOptions) { o.permissionsDisabled = true }
}

// WithDangerouslySkipPermissions auto-approves every AskUser escalation in
// the main loop — no approval request is emitted and nothing blocks waiting
// for an answer. For sandboxed environments (Docker containers, CI
// pipelines) where no human can respond to approval prompts.
//
// Difference from WithPermissionsDisabled: that option removes the
// permissions extension itself, so the default policy never escalates — but
// any other hook (a caller-supplied extension via WithExtension) can still
// escalate to AskUser, and unattended those escalations are denied. This
// option instead leaves every hook in place and flips the outcome: whatever
// escalates to AskUser is approved. Deny decisions (policy denylist,
// read-only mode, tool-call gates) still deny — this skips the asking, not
// the blocking. The two compose independently.
func WithDangerouslySkipPermissions() HarnessOption {
	return func(o *harnessOptions) { o.dangerouslySkipPermissions = true }
}

// WithApprovalTimeout bounds how long an AskUser tool call waits for an
// approval response before being denied. Default 120s; 0 denies immediately
// (unattended drivers with nobody to answer).
func WithApprovalTimeout(d time.Duration) HarnessOption {
	return func(o *harnessOptions) { o.approvalTimeout = d }
}

// WithBudgets registers the budgets extension for the main loop: the turn
// terminates gracefully (TurnResult.Terminated, surfaced by RunTurn as a
// "terminated: ..." error) when any limit is exceeded. Zero fields are
// unlimited.
func WithBudgets(l budgets.Limits) HarnessOption {
	return func(o *harnessOptions) { o.budgetLimits = l }
}

// WithPromptTemplatesDir registers an additional prompt-template directory
// (repeatable). Templates are *.md files invoked as "/name args..." in a
// RunTurn query, with bash-style argument substitution ($1, $@, ${1:-def},
// ${@:N:L}). Later directories override earlier ones on name collision.
// Nonexistent directories are skipped at discovery time.
func WithPromptTemplatesDir(dir string) HarnessOption {
	return func(o *harnessOptions) {
		o.promptTemplateDirs = append(o.promptTemplateDirs, dir)
	}
}

// WithContextFilesDisabled turns off automatic AGENTS.md loading into the
// system prompt.
func WithContextFilesDisabled() HarnessOption {
	return func(o *harnessOptions) {
		o.contextFilesDisabled = true
	}
}

// WithToolCallGate installs a gate consulted before every tool call — the
// main agent's and all subagents' (one shared gate, implemented as a
// core.Extension ToolCallHook). Returning a non-nil error blocks the call;
// the error string is fed back to the model as the tool result so it can
// adapt.
func WithToolCallGate(gate ToolCallGate) HarnessOption {
	return func(o *harnessOptions) {
		o.toolGate = gate
	}
}

// WithReadOnly denies every tool call whose tool is not marked read-only
// (tooldef.ReadOnlyReporter / ToolSpec.ReadOnly; unmarked and unknown tools
// count as mutating) — main agent and all subagents. spawn_agent is exempt:
// child loops carry the same gate, so spawned agents cannot mutate the
// filesystem (shared in-memory blackboard state may still change). Denials
// are instant error results ("read-only mode"), never approval prompts.
//
// This mode REPLACES the permissions extension (WithPermissionPolicy is
// ignored) so no AskUser escalation can shadow the marker rule — which also
// means an MCP-origin tool's own read-only claim is trusted here.
func WithReadOnly() HarnessOption {
	return func(o *harnessOptions) { o.readOnly = true }
}

// WithSessionDir relocates message-level session persistence (default
// <UserConfigDir>/tenzing/sessions).
func WithSessionDir(dir string) HarnessOption {
	return func(o *harnessOptions) { o.sessionDir = dir }
}

// WithSessionDisabled turns off message-level session persistence; the
// compression-summary memory files remain the only resume mechanism.
func WithSessionDisabled() HarnessOption {
	return func(o *harnessOptions) { o.sessionDisabled = true }
}

// WithThinking toggles model reasoning for the main agent's requests.
// Without this option the provider default applies.
func WithThinking(enabled bool) HarnessOption {
	return func(o *harnessOptions) { o.thinking = &enabled }
}

// WithThinkingBudget caps reasoning tokens per LLM call for the main agent.
// Exact on Anthropic (budget_tokens), tiered to reasoning_effort on
// OpenAI-compatible providers, mapped to a think level on Ollama. It wins over
// a model entry's reasoning_effort; an explicit WithThinking(false) leaves it
// inert. Values below 1 are ignored.
func WithThinkingBudget(tokens int64) HarnessOption {
	return func(o *harnessOptions) {
		if tokens > 0 {
			o.thinkingBudget = &tokens
		}
	}
}

// WithLLMRetry tunes the default agent's transient-LLM-error retry policy:
// max attempts (negative disables) and the base backoff delay. Ignored by
// custom agent builders.
func WithLLMRetry(max int, baseDelay time.Duration) HarnessOption {
	return func(o *harnessOptions) {
		o.llmRetryMax = max
		o.llmRetryBaseDelay = baseDelay
	}
}

// WithCompressionThreshold overrides the auto-compress trigger point as a
// fraction of the model's context window (default 0.75). Values outside
// (0,1] are ignored.
func WithCompressionThreshold(frac float64) HarnessOption {
	return func(o *harnessOptions) { o.compressionThreshold = frac }
}

// WithCompressionKeepMessages overrides how many recent messages survive
// compression verbatim (default 6). Non-positive values are ignored.
func WithCompressionKeepMessages(n int) HarnessOption {
	return func(o *harnessOptions) { o.compressionKeepMessages = n }
}

// WithMCPServer mounts an external MCP server (stdio transport) as a dynamic
// tool source: its tools appear as "mcp__<server>__<tool>" and are re-listed
// at each turn boundary. MCP-origin tools require approval under the default
// permission policy. Repeat the option per server.
func WithMCPServer(cfg mcp.ServerConfig) HarnessOption {
	return func(o *harnessOptions) { o.mcpServers = append(o.mcpServers, cfg) }
}

// WithSystemOne registers the System One decision model extension: it gates
// tool calls, judges when the executor should consult its advisor, and routes
// the turn's model. cfg.Client is required — a nil one registers nothing, so
// a caller with no decision model configured can pass the zero value.
//
// resolveModel turns one of cfg.Routing.Candidates into a client. Pass nil
// when routing is off or cannot be applied; the choice is then judged and
// logged but never acted on.
//
// Its tool gate is a batch hook, which the loop runs before the per-call
// hooks (permissions, the advisor gate); the loop keeps the strictest
// decision, so the gate can add a prompt but never remove one.
func WithSystemOne(cfg systemone.Config, resolveModel func(alias string) (common.LLM, error)) HarnessOption {
	return func(o *harnessOptions) {
		o.systemOne = &cfg
		o.resolveModel = resolveModel
	}
}
