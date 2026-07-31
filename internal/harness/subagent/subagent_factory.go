package subagent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/contextstore"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/toolport"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/blackboard"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/budgets"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/builtins"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/reminders"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/todo"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness/runner"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const (
	defaultSubAgentMaxDepth      = 2
	defaultSubAgentMaxIterations = 30
)

// inlineResultMax is the largest subagent result returned verbatim; longer
// results are deposited on the blackboard and replaced with a preview.
const inlineResultMax = 2000

var _ AgentFactory = (*SubAgentFactory)(nil)

type SubAgentFactoryConfig struct {
	AgentLLM      common.LLM
	AgentBuilder  runner.AgentBuilder
	MaxDepth      int
	MaxIterations int
	Cwd           string
	Emitter       core.Emitter
	Blackboard    *blackboard.Blackboard
	// ParentID is the spawning runner's ID; child IDs chain from it
	// ("438314ea" -> "438314ea_085c6444"). Empty means no prefix.
	ParentID string
	// SkillsExt is the shared skills extension instance (read-only registry;
	// sharing across parent and children is safe). Nil means no skills.
	SkillsExt core.Extension
	// ExtraExtensions are shared extension instances every child (and
	// grandchild) loop registers — e.g. the harness's tool-call gate.
	ExtraExtensions []core.Extension
}

type SubAgentFactory struct {
	agentLLM        common.LLM
	agentBuilder    runner.AgentBuilder
	maxDepth        int
	currentDepth    int
	maxIterations   int
	cwd             string
	emitter         core.Emitter
	blackboard      *blackboard.Blackboard
	parentID        string
	skillsExt       core.Extension
	extraExtensions []core.Extension
}

// childID derives a hierarchical agent ID from the parent's:
// "<parent>_<hex>". It is at once the child's runner ID (so every event it
// emits carries it), its event label, and its blackboard slot (underscores
// satisfy the slot-name rules).
func (f *SubAgentFactory) childID() string {
	id := runner.NewID()
	if f.parentID == "" {
		return id
	}
	return f.parentID + "_" + id
}

func NewSubAgentFactory(cfg SubAgentFactoryConfig) *SubAgentFactory {
	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultSubAgentMaxDepth
	}
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = defaultSubAgentMaxIterations
	}
	return &SubAgentFactory{
		agentLLM:        cfg.AgentLLM,
		agentBuilder:    cfg.AgentBuilder,
		maxDepth:        maxDepth,
		currentDepth:    0,
		maxIterations:   maxIter,
		cwd:             cfg.Cwd,
		emitter:         cfg.Emitter,
		blackboard:      cfg.Blackboard,
		parentID:        cfg.ParentID,
		skillsExt:       cfg.SkillsExt,
		extraExtensions: cfg.ExtraExtensions,
	}
}

func (f *SubAgentFactory) SpawnAgent(ctx context.Context, task string, taskContext string) (string, error) {
	childDepth := f.currentDepth + 1
	slog.Info("[subagent] spawning", "depth", childDepth, "max_depth", f.maxDepth, "task_len", len(task))
	start := time.Now()
	agentID := f.childID()

	registry := f.buildChildToolRegistry(agentID)

	todoFile := todo.NewTodoStore()
	childExts := f.childExtensions(agentID, todoFile)

	systemPrompt := "You are a sub-agent. Complete the assigned task using your tools. " +
		"Be thorough but concise in your final answer — it will be returned to the orchestrating agent."
	if f.cwd != "" {
		systemPrompt += " The project working directory is " + f.cwd + " — relative paths resolve there; do not guess other locations."
	}
	if f.blackboard != nil {
		systemPrompt += " You share a persistent Python REPL (the repl tool) with other agents. " +
			"Your blackboard slot is bb['" + agentID + "'] — writes to any other top-level slot " +
			"raise PermissionError; read anything; never busy-wait on another agent's slot."
	}
	// Extension prompt fragments (skills index, …) are appended here, same as
	// the composition root does for the main agent.
	if frags := childExts.PromptFragments(); len(frags) > 0 {
		systemPrompt += "\n\n" + strings.Join(frags, "\n\n")
	}

	childAgent, err := f.agentBuilder(f.agentLLM, systemPrompt)
	if err != nil {
		return "", fmt.Errorf("create child agent: %w", err)
	}

	// The child's composite mounts its registry plus extension tools
	// (list_skills/load_skill from the shared skills extension).
	childPort, err := toolport.NewComposite(registry, childExts)
	if err != nil {
		return "", fmt.Errorf("build child tool port: %w", err)
	}

	// Each child owns a fresh, isolated context store — no InitialMemory
	// (children don't resume prior sessions), scoped to its own runner ID.
	childStore := contextstore.New(contextstore.Config{
		LLM:      f.agentLLM,
		Emitter:  f.emitter,
		RunnerID: agentID,
	})

	childRunner, err := runner.NewAgentRunner(
		childAgent,
		runner.WithToolRegistry(registry),
		runner.WithToolPort(childPort),
		runner.WithContextStore(childStore),
		runner.WithSystemPrompt(systemPrompt),
		runner.WithExtensions(childExts),
		// Share the parent's emitter so the sub-agent's tool/LLM events reach
		// the same bus (and UI). Its events carry the child's RunnerID.
		runner.WithEmitter(f.emitter),
		// The hierarchical agent ID is the runner ID: events, logs, and the
		// blackboard slot all use the same identifier.
		runner.WithID(agentID),
	)
	if err != nil {
		return "", fmt.Errorf("create child runner: %w", err)
	}

	if f.emitter != nil {
		f.emitter.Emit(core.SubagentStartedEvent{
			BaseEvent: core.NewBaseEvent(core.EventSubagentStarted, childRunner.ID()),
			AgentID:   agentID,
			Prompt:    task,
		})
	}

	input := task
	if taskContext != "" {
		input = task + "\n\nContext:\n" + taskContext
	}
	if f.blackboard != nil {
		// Inoculate against orchestrator-invented slot names: task prompts
		// have instructed children to deposit under mnemonic slots
		// (bb['agents_md']), stranding results where nothing looks for them.
		input += "\n\n[Blackboard] Deposit results ONLY in bb['" + agentID + "']. " +
			"If the task above names any other bb slot, use bb['" + agentID + "'] instead."
	}

	result, err := childRunner.RunLoop(ctx, input)
	duration := time.Since(start)
	if err != nil {
		slog.Error("[subagent] failed", "depth", childDepth, "duration", duration.Round(time.Millisecond), "error", err)
		if f.emitter != nil {
			f.emitter.Emit(core.SubagentStoppedEvent{
				BaseEvent: core.NewBaseEvent(core.EventSubagentStopped, childRunner.ID()),
				AgentID:   agentID,
				Duration:  duration.Round(time.Millisecond),
			})
		}
		return "", err
	}

	slog.Info("[subagent] completed", "depth", childDepth, "duration", duration.Round(time.Millisecond), "answer_len", len(result))
	if f.emitter != nil {
		f.emitter.Emit(core.SubagentStoppedEvent{
			BaseEvent: core.NewBaseEvent(core.EventSubagentStopped, childRunner.ID()),
			AgentID:   agentID,
			Duration:  duration.Round(time.Millisecond),
		})
	}
	if f.blackboard == nil {
		return result, nil
	}
	// Always deposit, regardless of size: the completion message names the
	// canonical slot, so that slot must actually exist for the orchestrator.
	pv, depositErr := f.blackboard.Deposit(ctx, agentID, "result", result)
	if depositErr != nil {
		slog.Warn("[subagent] blackboard deposit failed, returning result inline",
			"agent_id", agentID, "error", depositErr)
		return result, nil
	}
	if len(result) <= inlineResultMax {
		return "Sub-agent " + agentID + " completed (blackboard slot bb['" + agentID + "']). " + result, nil
	}
	return "Sub-agent " + agentID + " completed. " + pv.String(), nil
}

// childExtensions assembles a child's extension set: its own todo reminders,
// the shared skills extension, the shared blackboard under the child's slot
// ID, and — below max depth — its own subagent spawner. Depth exclusion
// lives here: a child at max depth simply gets no spawn extension. The child
// runner never runs session hooks, so the blackboard ext's SessionEnd cannot
// close the shared REPL.
func (f *SubAgentFactory) childExtensions(agentID string, todoFile *todo.TodoFile) *core.Extensions {
	childDepth := f.currentDepth + 1
	exts := []core.Extension{reminders.New(todoFile.FormatReminder)}
	if f.maxIterations > 0 {
		// The child's iteration cap is a budget: graceful termination via
		// TurnResult.Terminated rather than an ad-hoc loop counter.
		exts = append(exts, budgets.New(budgets.Limits{MaxIterations: f.maxIterations}))
	}
	if f.skillsExt != nil {
		exts = append(exts, f.skillsExt)
	}
	exts = append(exts, f.extraExtensions...)
	if f.blackboard != nil {
		exts = append(exts, blackboard.NewExt(f.blackboard, agentID))
	}
	if childDepth < f.maxDepth {
		childFactory := &SubAgentFactory{
			agentLLM:        f.agentLLM,
			agentBuilder:    f.agentBuilder,
			maxDepth:        f.maxDepth,
			currentDepth:    childDepth,
			maxIterations:   f.maxIterations,
			cwd:             f.cwd,
			emitter:         f.emitter,
			blackboard:      f.blackboard,
			parentID:        agentID,
			skillsExt:       f.skillsExt,
			extraExtensions: f.extraExtensions,
		}
		exts = append(exts, NewSpawnExt(childFactory))
	}
	return core.NewExtensions(exts...)
}

func (f *SubAgentFactory) buildChildToolRegistry(agentID string) *toolport.Registry {
	toolRegistry := toolport.NewRegistry(f.cwd)
	for _, def := range builtins.Defaults() {
		toolRegistry.Register(def)
	}
	return toolRegistry
}
