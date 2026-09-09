package harness

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/agent"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/advisor"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/blackboard"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/mcp"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/prompts"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/reminders"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/skills"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/prompttmpl"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/runner"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/session"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/subagent"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

type Harness struct {
	mainAgentRunner *runner.AgentRunner
	toolPort        *toolport.Composite
	todoFile        *todo.TodoFile
	eventBus        *eventbus.EventBus
	stopHooks       func()
	stopMemoryHook  func()
	extensions      *core.Extensions

	// Runtime-control state: the main brain and context store (for
	// SetLLM/SetThinking/Compact) and the currently active main model.
	mainAgent    core.Agent
	mainStore    *contextstore.Store
	modelMu      sync.Mutex
	currentModel common.Model

	// Session persistence (nil/empty when WithSessionDisabled).
	// conversationID is the on-disk identity, which Clear and Resume rotate;
	// the runner ID (the event-stream identity) is fixed for the harness's
	// life. sessionMu guards the three fields the rotation swaps.
	sessionMu      sync.Mutex
	sessionStore   *session.Store
	stopSession    func()
	sessionDir     string
	conversationID string
	cwd            string

	promptTemplates *prompttmpl.Registry
	skills          *skills.Registry

	// context records which files fed the main system prompt, and whether
	// they overran contextFilesMaxBytes — in which case the tail of the last
	// file(s) never reached the prompt even though every path is reported.
	context contextLoad

	shutdownOnce sync.Once
}

// EventBus returns the harness event bus. It is always non-nil.
func (h *Harness) EventBus() *eventbus.EventBus {
	return h.eventBus
}

// defaultAgentBuilder adapts the built-in agent implementation to the
// runner.AgentBuilder contract.
func defaultAgentBuilder(llm common.LLM, systemPrompt string) (core.Agent, error) {
	return agent.New(agent.AgentConfig{Model: llm, SystemPrompt: systemPrompt})
}

func New(mainLLM common.LLM, opts ...HarnessOption) (*Harness, error) {
	if mainLLM == nil {
		return nil, fmt.Errorf("main LLM is required")
	}

	o := defaultHarnessOptions()
	for _, opt := range opts {
		opt(o)
	}

	brain := o.agentBuilder
	if brain == nil {
		brain = defaultAgentBuilder
	}

	// Resolve role clients: unset roles fall back to the main LLM.
	subagentLLM := o.subagentLLM
	if subagentLLM == nil {
		subagentLLM = mainLLM
	}

	// blackboardLLM serves the shared blackboard REPL's llm_query/llm_batch
	// sub-LM calls.
	blackboardLLM := o.blackboardLLM
	if blackboardLLM == nil {
		blackboardLLM = mainLLM
	}

	// get cwd
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("could not determine default cwd: %w", err)
	}

	// register hooks to event bus
	var stopHooks func()
	if !hooksEmpty(o.hooks) {
		stopHooks = eventbus.StartHooks(o.eventBus, o.hooks)
	}

	// set up todo store
	todoFile := todo.NewTodoStore()
	todoFile.SetEmitter(o.eventBus)

	// Prompt templates: *.md slash commands expanded before the loop sees
	// the query. No default dirs; callers register via WithPromptTemplatesDir.
	promptTemplates := prompttmpl.NewRegistry()
	for _, dir := range o.promptTemplateDirs {
		promptTemplates.AddDir(dir)
	}

	// build skills registry
	skillsRegistry := skills.NewRegistry()
	for _, skillDir := range o.skillDirs {
		skillsRegistry.RegisterSkillDir(skillDir)
	}
	for _, pluginDir := range o.pluginDirs {
		skillsRegistry.RegisterPluginDir(pluginDir)
	}
	skillsExt := skills.NewExt(skillsRegistry)

	// The blackboard instance lives at the composition root and is SHARED:
	// the main agent and every subagent wrap the same instance in their own
	// blackboard.Ext under their own agent ID. It is always on — the Python
	// process boots lazily on first use, so an unused blackboard costs
	// nothing.
	bb := blackboard.New(blackboard.Config{
		Querier:    blackboard.NewLLMQuerier(blackboardLLM),
		WorkingDir: cwd,
	})

	// Conversation identity: resume under the supplied ID or start fresh.
	// Pre-generated so the subagent factory (built before the runner) can
	// derive hierarchical child IDs from it: "<mainID>_<hex>".
	mainRunnerID := o.conversationID
	if mainRunnerID == "" {
		mainRunnerID = runner.NewID()
	}

	// default extensions run first, then any caller-supplied via WithExtension.
	// Permissions come FIRST so later hooks observe (and may escalate, never
	// lower) its decision.
	var defaultExts []core.Extension
	// Read-only mode REPLACES the permissions extension: one rule — tools
	// marked read-only (plus spawn_agent, whose children are gated by the
	// same hook) run, everything else is denied outright. No AskUser
	// escalation survives to shadow that rule, and the approval timeout is
	// forced to 0 so nothing can block. The classifier is late-bound to the
	// composite ToolPort below. Shared with subagent loops.
	var roExt *readOnlyExt
	switch {
	case o.readOnly:
		o.approvalTimeout = 0
		roExt = &readOnlyExt{}
		defaultExts = append(defaultExts, roExt)
	case !o.permissionsDisabled:
		policy := permissions.DefaultPolicy()
		if o.permissionPolicy != nil {
			policy = *o.permissionPolicy
		}
		defaultExts = append(defaultExts, permissions.New(policy))
	}
	// The tool-call gate runs right after permissions; the same extension
	// instance is shared with every subagent loop.
	var gateExt core.Extension
	if o.toolGate != nil {
		gateExt = &toolCallGateExt{gate: o.toolGate}
		defaultExts = append(defaultExts, gateExt)
	}
	// The advisor write-gate runs after permissions/read-only so it can only
	// escalate their decisions. Registered only when the advisor is enabled;
	// its read-only classifier is late-bound to the composite below.
	var advisorGate *advisor.GateExt
	if o.advisorLLM != nil {
		advisorGate = advisor.NewGateExt(o.advisorNudge, o.advisorExemptTools...)
		defaultExts = append(defaultExts, advisorGate)
	}
	defaultExts = append(defaultExts,
		reminders.New(todoFile.FormatReminder),
		skillsExt,
	)
	if o.budgetLimits != (budgets.Limits{}) {
		defaultExts = append(defaultExts, budgets.New(o.budgetLimits))
	}
	if len(o.mcpServers) > 0 {
		defaultExts = append(defaultExts, mcp.New(o.mcpServers...))
	}
	defaultExts = append(defaultExts, blackboard.NewExt(bb, "main"))
	if o.subagentMaxDepth > 0 {
		var childExtras []core.Extension
		if roExt != nil {
			childExtras = append(childExtras, roExt)
		}
		if gateExt != nil {
			childExtras = append(childExtras, gateExt)
		}
		subagentFactory := subagent.NewSubAgentFactory(subagent.SubAgentFactoryConfig{
			AgentLLM:        subagentLLM,
			AgentBuilder:    brain,
			MaxDepth:        o.subagentMaxDepth,
			MaxIterations:   o.subagentMaxIterations,
			Cwd:             cwd,
			Emitter:         o.eventBus,
			Blackboard:      bb,
			ParentID:        mainRunnerID,
			SkillsExt:       skillsExt,
			ExtraExtensions: childExtras,
		})
		defaultExts = append(defaultExts, subagent.NewSpawnExt(subagentFactory))
	}
	allExts := core.NewExtensions(append(defaultExts, o.extensions...)...)

	toolRegistry := toolport.NewRegistry(cwd)
	for _, def := range builtins.Defaults() {
		toolRegistry.Register(def)
	}
	toolRegistry.RegisterFromProvider(todoFile)

	// Memory: the harness owns persistence. Summaries arrive on the event
	// bus (ContextCompressedEvent) and are written per-conversation; the
	// agent layers never touch files.
	memory := newMemoryStore(mainRunnerID, time.Now)
	memory.sweep(memoryTTL, o.conversationID)

	var initialMemory string
	if o.conversationID != "" {
		initialMemory = memory.loadLatest(o.conversationID)
		if initialMemory == "" {
			slog.Warn("no memory found for conversation, starting fresh", "conversation_id", o.conversationID)
		}
	}

	stopMemoryHook := eventbus.StartHooks(o.eventBus, eventbus.Hooks{
		OnContextCompressed: func(e core.ContextCompressedEvent) {
			memory.persist(e.RunnerID, e.Summary)
		},
	})

	// Session persistence: message-level JSONL log per conversation. On
	// resume, the reconstructed history supersedes the memory summary; the
	// summary stays as fallback for conversations without a session file.
	var initialHistory []common.Message
	var loadedThinking *bool
	var sessionStore *session.Store
	var stopSession func()
	var sessionDir string
	if !o.sessionDisabled {
		sessionDir = o.sessionDir
		if sessionDir == "" {
			sessionDir = session.DefaultDir()
		}
		if o.conversationID != "" {
			res, err := session.Load(sessionDir, cwd, o.conversationID)
			switch {
			case err != nil:
				slog.Warn("session load failed, falling back to memory summary", "error", err)
			case res != nil:
				initialHistory = res.History
				loadedThinking = res.Thinking
				if len(res.Tasks) > 0 {
					_ = todoFile.WriteTasks(res.Tasks)
				}
				slog.Info("session resumed", "path", res.Path, "messages", len(res.History), "tasks", len(res.Tasks))
			}
		}
		sessionStore = session.NewStore(sessionDir, cwd, mainRunnerID, mainLLM.GetModel().GetName(), time.Now)
		stopSession = session.StartPersister(o.eventBus, sessionStore, mainRunnerID, func() []todo.Task {
			tasks, _ := todoFile.ReadTasks()
			return tasks
		})
	}

	// The main context store is built before the advisor tool so the advisor
	// can read the live conversation (its transcript view) at call time. The
	// runner receives the same store below.
	mainStore := contextstore.New(contextstore.Config{
		LLM:                     mainLLM,
		InitialMemory:           initialMemory,
		InitialHistory:          initialHistory,
		Emitter:                 o.eventBus,
		RunnerID:                mainRunnerID,
		CompressionThreshold:    o.compressionThreshold,
		CompressionKeepMessages: o.compressionKeepMessages,
	})

	if o.advisorLLM != nil {
		toolRegistry.Register(advisor.NewAdvisorTool(o.advisorLLM, mainStore.Messages))
	}

	for _, tool := range o.extraTools {
		toolRegistry.Register(tool)
	}

	if len(o.disabledTools) > 0 {
		toolRegistry = toolRegistry.CopyWithout(o.disabledTools...)
	}

	// Resolve the system prompt once: the agent (wire) and the runner
	// (logging/accessor) must see the same string. Leaving it to the runner's
	// own fallback sent requests with an empty system prompt while the log
	// showed the default.
	mainSystemPrompt := o.mainSystemPrompt
	if mainSystemPrompt == "" {
		mainSystemPrompt = prompts.DefaultSystemPrompt()
	}
	// AGENTS.md context files: global + ancestor chain, appended after the
	// base prompt (main agent only; subagents keep their focused prompts).
	var ctx contextLoad
	if !o.contextFilesDisabled {
		if ctx = loadContextFiles(cwd); ctx.content != "" {
			mainSystemPrompt += ctx.content
		}
	}
	// Extension prompt fragments (skills index, …) are appended at the
	// composition root — the agent no longer assembles any of its prompt.
	// Registration order = fragment order (cache-stable).
	if frags := allExts.PromptFragments(); len(frags) > 0 {
		mainSystemPrompt = mainSystemPrompt + "\n\n" + strings.Join(frags, "\n\n")
	}

	// Build and wire the main agent. The default builder path is stateless;
	// custom builders own their own memory story (and skip the retry /
	// thinking config, which belongs to the built-in implementation).
	var mainAgent core.Agent
	if o.agentBuilder != nil {
		mainAgent, err = o.agentBuilder(mainLLM, mainSystemPrompt)
	} else {
		mainAgent, err = agent.New(agent.AgentConfig{
			Model:          mainLLM,
			SystemPrompt:   mainSystemPrompt,
			Think:          o.thinking,
			RetryMax:       o.llmRetryMax,
			RetryBaseDelay: o.llmRetryBaseDelay,
			OnLLMRetry: func(attempt, maxRetries int, retryErr error, delay time.Duration) {
				o.eventBus.Emit(core.LLMRetryEvent{
					BaseEvent:  core.NewBaseEvent(core.EventLLMRetry, mainRunnerID),
					Attempt:    attempt,
					MaxRetries: maxRetries,
					Error:      retryErr.Error(),
					Delay:      delay,
				})
			},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("build main agent: %w", err)
	}

	// The composite ToolPort mounts the native registry plus any extension
	// tool sources; the loop snapshots it each turn and passes definitions
	// per reasoning call.
	composite, err := toolport.NewComposite(toolRegistry, allExts)
	if err != nil {
		return nil, fmt.Errorf("build tool port: %w", err)
	}
	if roExt != nil {
		roExt.classify = composite.ReadOnly
	}
	if advisorGate != nil {
		advisorGate.SetClassifier(composite.ReadOnly)
	}

	// create agent runner
	runnerOpts := []runner.AgentRunnerOption{
		runner.WithID(mainRunnerID),
		runner.WithToolRegistry(toolRegistry),
		runner.WithToolPort(composite),
		runner.WithContextStore(mainStore),
		runner.WithEmitter(o.eventBus),
		runner.WithTextDeltaHandler(o.onTextDelta),
		runner.WithThinkingDeltaHandler(o.onThinkingDelta),
		runner.WithSystemPrompt(mainSystemPrompt),
		runner.WithExtensions(allExts),
		runner.WithApprovalTimeout(o.approvalTimeout),
	}
	if o.dangerouslySkipPermissions {
		runnerOpts = append(runnerOpts, runner.WithSkipPermissions())
	}
	mainAgentRunner, err := runner.NewAgentRunner(mainAgent, runnerOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize runner: %w", err)
	}

	// A resumed session's thinking state carries over unless the caller set
	// WithThinking explicitly.
	if o.thinking == nil && loadedThinking != nil {
		if t, ok := mainAgent.(interface{ SetThinking(bool) }); ok {
			t.SetThinking(*loadedThinking)
		}
	}

	// Session-start hooks are load-bearing: an extension that cannot start
	// (MCP connect, later) fails harness construction.
	if err := allExts.RunSessionStart(context.Background()); err != nil {
		return nil, fmt.Errorf("session start hooks: %w", err)
	}

	return &Harness{
		mainAgentRunner: mainAgentRunner,
		toolPort:        composite,
		todoFile:        todoFile,
		eventBus:        o.eventBus,
		stopHooks:       stopHooks,
		stopMemoryHook:  stopMemoryHook,
		extensions:      allExts,
		mainAgent:       mainAgent,
		mainStore:       mainStore,
		currentModel:    mainLLM.GetModel(),
		sessionStore:    sessionStore,
		stopSession:     stopSession,
		conversationID:  mainRunnerID,
		sessionDir:      sessionDir,
		cwd:             cwd,
		promptTemplates: promptTemplates,
		skills:          skillsRegistry,
		context:         ctx,
	}, nil
}

// errBusy is returned by between-turns operations invoked mid-turn.
var errBusy = fmt.Errorf("a turn is running; try again when the agent is idle")

// idle reports whether no turn is in flight on the main runner.
func (h *Harness) idle() bool {
	s := h.mainAgentRunner.LoopState()
	return s == string(core.LoopStateStarted) || s == string(core.LoopStateStopped)
}

// Compact forces context compression now, bypassing the auto thresholds.
// instructions optionally steer the summary. Only between turns.
func (h *Harness) Compact(ctx context.Context, instructions string) error {
	if !h.idle() {
		return errBusy
	}
	if _, err := h.mainStore.Compact(ctx, instructions); err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	return nil
}

// SetLLM switches the main agent to a different LLM client between turns.
// The caller owns client construction (and any reuse/caching). Subagent and
// blackboard roles keep their clients.
func (h *Harness) SetLLM(llm common.LLM) error {
	if !h.idle() {
		return errBusy
	}
	if llm == nil {
		return fmt.Errorf("LLM is required")
	}
	setter, ok := h.mainAgent.(interface{ SetLLM(common.LLM) })
	if !ok {
		return fmt.Errorf("the configured agent does not support model switching")
	}
	from := h.mainAgent.GetCurrentModel()
	setter.SetLLM(llm)
	h.mainStore.SetLLM(llm)
	h.modelMu.Lock()
	h.currentModel = llm.GetModel()
	h.modelMu.Unlock()
	h.eventBus.Emit(core.ModelChangedEvent{
		BaseEvent: core.NewBaseEvent(core.EventModelChanged, h.mainAgentRunner.ID()),
		From:      from,
		To:        llm.GetModel().GetName(),
	})
	return nil
}

// SetThinking toggles model reasoning for the main agent between turns.
func (h *Harness) SetThinking(enabled bool) error {
	if !h.idle() {
		return errBusy
	}
	setter, ok := h.mainAgent.(interface{ SetThinking(bool) })
	if !ok {
		return fmt.Errorf("the configured agent does not support thinking control")
	}
	setter.SetThinking(enabled)
	h.eventBus.Emit(core.ThinkingChangedEvent{
		BaseEvent: core.NewBaseEvent(core.EventThinkingChanged, h.mainAgentRunner.ID()),
		Enabled:   enabled,
	})
	return nil
}

// CurrentModel returns the main agent's active model (tracking SetLLM
// switches).
func (h *Harness) CurrentModel() common.Model {
	h.modelMu.Lock()
	defer h.modelMu.Unlock()
	return h.currentModel
}

// Shutdown stops hook dispatch, closes session persistence, and runs the
// extensions' session-end hooks. Safe to call more than once.
func (h *Harness) Shutdown() {
	h.shutdownOnce.Do(func() {
		if h.stopHooks != nil {
			h.stopHooks()
		}
		if h.stopMemoryHook != nil {
			h.stopMemoryHook()
		}
		// Under sessionMu: a concurrent Clear/Resume rotation swaps both.
		h.sessionMu.Lock()
		if h.stopSession != nil {
			h.stopSession()
		}
		if h.sessionStore != nil {
			h.sessionStore.Close()
		}
		h.sessionMu.Unlock()
		// Session-end hooks (blackboard close, …) degrade on error and get a
		// bounded window to clean up.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.extensions.RunSessionEnd(ctx)
	})
}

// SessionInfo returns the resolved session directory and the working
// directory sessions are keyed by. dir is "" when persistence is disabled.
func (h *Harness) SessionInfo() (dir, cwd string) {
	return h.sessionDir, h.cwd
}

func (h *Harness) GetCurrentModel() string {
	return h.mainAgentRunner.GetCurrentModel()
}

// ToolDefinitions returns the full mounted tool surface — native registry
// tools plus extension-provided tools — as the model sees it.
// ContextFiles returns the AGENTS.md paths loaded into the main system prompt,
// in the order they were appended. Empty when none were found or when
// WithContextFilesDisabled was set. Callers must not mutate the result.
func (h *Harness) ContextFiles() []string {
	return h.context.files
}

// RuleFiles returns the ~/.claude/rules/*.md paths loaded into the main system
// prompt. Callers must not mutate the result.
func (h *Harness) RuleFiles() []string {
	return h.context.rules
}

// ContextFilesTruncated reports whether the loaded context files overran the
// size cap, meaning the prompt holds less than ContextFiles implies.
func (h *Harness) ContextFilesTruncated() bool {
	return h.context.truncated
}

// Skills returns the discovered skills as name -> description, the same index
// the agent sees in its system prompt. Callers must not mutate the result.
func (h *Harness) Skills() map[string]string {
	return h.skills.GetSkillMap()
}

func (h *Harness) ToolDefinitions() []common.ToolDefinition {
	return h.toolPort.Definitions()
}

func (h *Harness) SystemPrompt() string {
	return h.mainAgentRunner.SystemPrompt()
}

// ConversationID is the handle for resuming this conversation later via
// WithConversationID. It starts as the main agent's runner ID and moves
// when Clear or Resume rotates the session record.
func (h *Harness) ConversationID() string {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	return h.conversationID
}

// Clear drops the conversation history and starts a new session record, so
// the cleared transcript stays on disk under the old ID and the harness
// continues under a fresh one. Only between turns.
func (h *Harness) Clear() error {
	if !h.idle() {
		return errBusy
	}
	h.mainStore.Clear()
	h.rotateSession(runner.NewID())
	return nil
}

// Resume loads a persisted conversation over the live one: its history
// replaces the current context, its thinking preference is reapplied, its
// todo list is restored, and persistence continues appending to that
// conversation's own record. Only between turns.
func (h *Harness) Resume(conversationID string) error {
	if !h.idle() {
		return errBusy
	}
	if conversationID == "" {
		return fmt.Errorf("conversation ID is required")
	}
	if h.sessionDir == "" {
		return fmt.Errorf("session persistence is disabled")
	}

	res, err := session.Load(h.sessionDir, h.cwd, conversationID)
	if err != nil {
		return fmt.Errorf("load session %s: %w", conversationID, err)
	}
	if res == nil {
		return fmt.Errorf("no session %s recorded for this directory", conversationID)
	}

	h.mainStore.Replace(res.History)
	if len(res.Tasks) > 0 {
		if err := h.todoFile.WriteTasks(res.Tasks); err != nil {
			slog.Warn("session resume: restoring todo list failed", "error", err)
		}
	}
	h.rotateSession(conversationID)
	// Thinking last: it emits an event, so it should not fire before the
	// history and session identity are the resumed conversation's.
	if res.Thinking != nil {
		if err := h.SetThinking(*res.Thinking); err != nil {
			slog.Warn("session resume: restoring thinking preference failed", "error", err)
		}
	}
	slog.Info("session resumed live", "conversation_id", conversationID, "messages", len(res.History), "tasks", len(res.Tasks))
	return nil
}

// rotateSession points persistence at conversation id: the old record is
// closed and a new store opened (appending, when a file for id already
// exists). The persister is restarted against the unchanged runner ID — it
// filters events by that, not by the conversation identity. A harness with
// persistence disabled only records the new ID.
func (h *Harness) rotateSession(id string) {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	h.conversationID = id
	if h.sessionStore == nil {
		return
	}
	if h.stopSession != nil {
		h.stopSession()
	}
	h.sessionStore.Close()
	h.sessionStore = session.NewStore(h.sessionDir, h.cwd, id, h.CurrentModel().GetName(), time.Now)
	h.stopSession = session.StartPersister(h.eventBus, h.sessionStore, h.mainAgentRunner.ID(), func() []todo.Task {
		tasks, _ := h.todoFile.ReadTasks()
		return tasks
	})
}

// RunTurn runs one agent turn. A query invoking a registered prompt
// template ("/name args...", WithPromptTemplatesDir) is expanded first; an
// unknown "/name" fails the turn without an LLM call.
func (h *Harness) RunTurn(ctx context.Context, query string) (string, error) {
	expanded, err := h.promptTemplates.Preprocess(query)
	if err != nil {
		return "", err
	}
	if expanded != query {
		slog.Debug("prompt template expanded", "query", query)
	}
	return h.mainAgentRunner.RunLoop(ctx, expanded)
}

// ErrVisionUnsupported is returned by RunTurnWithImages when the current
// main model does not accept image content.
var ErrVisionUnsupported = fmt.Errorf("the current model does not support image input")

// SupportsVision reports whether the current main model accepts image
// content blocks (tracks SetLLM switches).
func (h *Harness) SupportsVision() bool {
	return h.CurrentModel().GetSupportsVision()
}

// RunTurnWithImages runs one agent turn with images attached to the query
// message. Data is raw base64 (no data: URI prefix). Fails fast — before any
// API call — with ErrVisionUnsupported when the current model lacks vision.
func (h *Harness) RunTurnWithImages(ctx context.Context, query string, images []common.ImageSource) (string, error) {
	expanded, err := h.promptTemplates.Preprocess(query)
	if err != nil {
		return "", err
	}
	if expanded != query {
		slog.Debug("prompt template expanded", "query", query)
	}
	query = expanded
	if len(images) > 0 {
		if !h.SupportsVision() {
			return "", fmt.Errorf("%w (model %q)", ErrVisionUnsupported, h.GetCurrentModel())
		}
		// Emitted before the turn so the session persister logs the images
		// (sidecar blobs) ahead of the user entry.
		evImages := make([]core.ImageData, len(images))
		for i, img := range images {
			evImages[i] = core.ImageData{MediaType: img.MediaType, Data: img.Data}
		}
		h.eventBus.Emit(core.ImagesAttachedEvent{
			BaseEvent: core.NewBaseEvent(core.EventImagesAttached, h.mainAgentRunner.ID()),
			Images:    evImages,
		})
	}
	return h.mainAgentRunner.RunLoopWithImages(ctx, query, images)
}

// Steer queues a user message for injection into the running main-agent
// loop at the next tool-execution boundary. Messages queued while idle are
// injected at the first boundary of the next turn. Errors when the steering
// buffer is full.
func (h *Harness) Steer(msg string) error {
	return h.mainAgentRunner.Steer(msg)
}

// LoopState reports the main-agent loop FSM's current state (e.g.
// "started", "stopped", "reasoning_started"). Safe for concurrent use.
func (h *Harness) LoopState() string {
	return h.mainAgentRunner.LoopState()
}

// hooksEmpty reports whether no hook callbacks are set in h.
func hooksEmpty(h eventbus.Hooks) bool {
	return h.OnSessionStarted == nil &&
		h.OnSessionEnded == nil &&
		h.OnTurnStarted == nil &&
		h.OnTurnCompleted == nil &&
		h.OnLoopStarted == nil &&
		h.OnLoopStopped == nil &&
		h.OnReasoningStarted == nil &&
		h.OnReasoningFinished == nil &&
		h.OnToolExecutionStarted == nil &&
		h.OnToolExecutionFinished == nil &&
		h.OnLLMResponse == nil &&
		h.OnToolSucceeded == nil &&
		h.OnToolFailed == nil &&
		h.OnToolDenied == nil &&
		h.OnToolProgress == nil &&
		h.OnContextCompressing == nil &&
		h.OnContextCompressed == nil &&
		h.OnError == nil &&
		h.OnApprovalRequested == nil &&
		h.OnSubagentStarted == nil &&
		h.OnSubagentStopped == nil &&
		h.OnTaskCreated == nil &&
		h.OnTaskCompleted == nil &&
		h.OnSteeringInjected == nil &&
		h.OnLLMRetry == nil &&
		h.OnModelChanged == nil &&
		h.OnThinkingChanged == nil &&
		h.OnImagesAttached == nil
}
