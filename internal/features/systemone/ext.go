// Package systemone lets a System One decision model (TypeSafe's Jev) make
// harness decisions: which tool calls to question, when the executor should
// consult its advisor, and which LLM serves the turn. It asks in two batches
// per iteration — one at BeforeIteration, one over the whole pending tool
// list — because a System One model ingests the state once and answers every
// question in the batch against it, so extra questions are near-free and
// extra requests are not.
//
// It is an advisor to the harness, never a dependency of it: an unreachable
// endpoint, a malformed answer, or an answer below its confidence floor all
// leave the harness doing exactly what it would have done without this
// extension. The extension only ever tightens a decision: its gate runs as a
// batch hook, before the per-call hooks (permissions, the advisor gate), and
// core keeps the strictest decision any hook reaches, so nothing it answers
// can lower theirs, nor theirs its.
package systemone

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

var (
	_ core.Extension           = (*Ext)(nil)
	_ core.BeforeIterationHook = (*Ext)(nil)
	_ core.ToolBatchHook       = (*Ext)(nil)
)

// Candidate is one model the router may choose, named by its config alias and
// described by that entry's `description:` — the text becomes the option's
// criteria, so whoever declares the model writes what it is for.
type Candidate struct {
	Name        string
	Description string
}

// GateConfig tunes the tool gate. Zero thresholds mean the Default* values.
// The working-directory rule has no threshold: it is a fact, not a judgment.
type GateConfig struct {
	Disabled        bool
	IrreversibleAsk float64
	SecretsAsk      float64
}

// AdvisorConfig tunes the advisor-need judgment. Disable it when the advisor
// tool is not mounted — there would be nothing to consult.
type AdvisorConfig struct {
	Disabled     bool
	ConsultAbove float64
}

// RoutingConfig tunes model routing. Fewer than two candidates disables it:
// there is nothing to choose between.
type RoutingConfig struct {
	Candidates    []Candidate
	Current       string // the alias serving now; choosing it again is a no-op
	MinConfidence float64
}

// DefaultBatchTimeout bounds one batch. Fail-open is only useful if it is
// also fail-fast: the protocol client retries transient failures five times
// with backoff, which measured ~22s per batch against an unreachable
// endpoint — two batches an iteration, so a dead endpoint would stall every
// iteration by the better part of a minute. The deadline cancels the retry
// ladder instead.
const DefaultBatchTimeout = 10 * time.Second

// maxConsecutiveFailures stops paying that price over and over. After this
// many failed batches in a row the extension stops calling for the rest of
// the turn; the next turn tries again, so a blip costs a turn's tail and an
// outage costs this many timeouts per turn rather than two per iteration.
const maxConsecutiveFailures = 3

// DefaultRecentMessages is the message tail a caller should use when it has
// no explicit setting. It is not applied by New: 0 means "send none", a real
// choice, so only a caller that can tell "unset" from "zero" can default it.
const DefaultRecentMessages = 4

// Config configures Ext. Client is required; everything else has a working
// default.
type Config struct {
	Client common.SystemOne
	// RecentMessages is how many trailing conversation messages ride along in
	// every state. Accuracy falls as the state fills with detail the decision
	// does not need, and this content leaves the machine — 0 sends none.
	RecentMessages int
	// Timeout bounds one batch, including the client's retries. 0 =
	// DefaultBatchTimeout; negative disables the deadline.
	Timeout time.Duration
	// WorkingDir is the directory tenzing was started in — the frame every
	// path fact is judged against. Empty means the process's own cwd.
	WorkingDir string
	// CaptureFile, when set, appends every batch — state, questions, answers
	// — as one JSON line, so a live judgment can be replayed in evals/
	// against a candidate wording. Hand-written fixtures measured 0.84 on a
	// deletion that scored 0.49 live; the only way to close that gap is to
	// replay what the model actually saw. Debug aid; leave empty in
	// production, the file holds conversation content.
	CaptureFile string
	Gate        GateConfig
	Advisor     AdvisorConfig
	Routing     RoutingConfig
}

// Ext is the extension. Its dependencies beyond the client are late-bound by
// harness.New: the composite ToolPort, the context store and the LLM switch
// are all built after the extension set, the same ordering advisor.GateExt
// solves with SetClassifier.
type Ext struct {
	client   common.SystemOne
	recentN  int
	timeout  time.Duration
	workdir  string
	tempdirs []string
	capture  string
	gate     GateConfig
	advisor  AdvisorConfig
	routing  RoutingConfig

	mu           sync.Mutex
	emitter      core.Emitter
	messages     func(context.Context) ([]common.Message, error)
	router       func(context.Context, string) error
	classify     func(name string) bool // true = read-only
	shellArgs    func(command string) []string
	runnerID     string
	request      string // the turn's opening user message, rendered; pinned into every tail
	advisorArmed bool   // a consult was judged due and has not happened yet
	failures     int    // consecutive failed batches; resets on success and each turn
	consults     int    // advisor calls issued this turn
}

// New builds the extension. A nil Client yields a nil extension so a caller
// can pass the result straight to harness.WithExtension without a branch.
func New(cfg Config) *Ext {
	if cfg.Client == nil {
		return nil
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultBatchTimeout
	}
	workdir := cfg.WorkingDir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	// Both frames are resolved once, here: comparing a resolved path against
	// an unresolved root is how /tmp (a symlink to /private/tmp on macOS)
	// reads as "not the temp directory".
	return &Ext{
		client:   cfg.Client,
		recentN:  cfg.RecentMessages,
		timeout:  timeout,
		workdir:  followSymlinks(workdir),
		tempdirs: tempRoots(),
		capture:  cfg.CaptureFile,
		gate:     withGateDefaults(cfg.Gate),
		advisor:  withAdvisorDefaults(cfg.Advisor),
		routing:  withRoutingDefaults(cfg.Routing),
	}
}

func withGateDefaults(c GateConfig) GateConfig {
	if c.IrreversibleAsk == 0 {
		c.IrreversibleAsk = DefaultIrreversibleAsk
	}
	if c.SecretsAsk == 0 {
		c.SecretsAsk = DefaultSecretsAsk
	}
	return c
}

func withAdvisorDefaults(c AdvisorConfig) AdvisorConfig {
	if c.ConsultAbove == 0 {
		c.ConsultAbove = DefaultAdvisorConsult
	}
	return c
}

func withRoutingDefaults(c RoutingConfig) RoutingConfig {
	if c.MinConfidence == 0 {
		c.MinConfidence = DefaultRoutingConfidence
	}
	return c
}

func (e *Ext) Name() string { return "systemone" }

// SetEmitter late-binds the event bus, as todo.TodoFile does.
func (e *Ext) SetEmitter(em core.Emitter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitter = em
}

// SetMessages late-binds the conversation source for the recent-message tail.
func (e *Ext) SetMessages(f func(context.Context) ([]common.Message, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = f
}

// SetRouter late-binds how a routing choice is applied — harness.SetLLM with
// the alias resolved through the model registry.
func (e *Ext) SetRouter(f func(context.Context, string) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.router = f
}

// SetClassifier late-binds the read-only classifier (composite.ReadOnly), so
// an armed advisor block still lets orientation through.
func (e *Ext) SetClassifier(f func(name string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.classify = f
}

// SetShellSplitter late-binds the shell word splitter used to find the paths
// a bash command names. It comes from the harness rather than an import
// because the parser lives under another feature (permissions/shell) and a
// feature may not reach into one. Unbound, bash calls simply report no
// paths.
func (e *Ext) SetShellSplitter(f func(command string) []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shellArgs = f
}

// routingEnabled reports whether there is a choice worth asking about.
func (e *Ext) routingEnabled() bool { return len(e.routing.Candidates) >= 2 }

// evaluate runs one batch and emits its event either way. A failure is
// reported as (zero response, false) — never an error — because every caller
// falls back rather than propagating.
func (e *Ext) evaluate(ctx context.Context, batch string, req common.EvaluationRequest) (common.EvaluationResponse, bool) {
	if e.tripped() {
		return common.EvaluationResponse{}, false
	}
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	start := time.Now()
	resp, err := e.client.Evaluate(ctx, req)
	if err != nil {
		e.recordFailure()
		slog.Warn("system one batch failed; falling back",
			"ext", e.Name(), "batch", batch, "error", err)
		e.emit(core.SystemOneDecisionEvent{
			BaseEvent: core.NewBaseEvent(core.EventSystemOneDecision, e.runner()),
			Batch:     batch,
			Duration:  time.Since(start),
			Error:     err.Error(),
		})
		return common.EvaluationResponse{}, false
	}
	e.mu.Lock()
	e.failures = 0
	e.mu.Unlock()
	e.record(batch, req, resp)
	return resp, true
}

// record appends one batch to the capture file. Best-effort: a capture that
// cannot be written is logged and dropped, never allowed to disturb the
// batch it describes.
func (e *Ext) record(batch string, req common.EvaluationRequest, resp common.EvaluationResponse) {
	if e.capture == "" {
		return
	}
	line, err := json.Marshal(map[string]any{
		"batch":     batch,
		"model":     resp.Model,
		"state":     req.State,
		"questions": req.Questions,
		"answers":   resp.Answers,
	})
	if err != nil {
		slog.Warn("system one capture: encode", "error", err)
		return
	}
	f, err := os.OpenFile(e.capture, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("system one capture: open", "path", e.capture, "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		slog.Warn("system one capture: write", "path", e.capture, "error", err)
	}
}

// tripped reports whether this turn has already paid for enough failures.
func (e *Ext) tripped() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.failures >= maxConsecutiveFailures
}

// recordFailure counts a failed batch and logs the moment the breaker opens,
// so a silent decision model is visible once rather than every batch.
func (e *Ext) recordFailure() {
	e.mu.Lock()
	e.failures++
	tripped := e.failures == maxConsecutiveFailures
	e.mu.Unlock()
	if tripped {
		slog.Warn("system one unreachable; no more batches this turn",
			"ext", e.Name(), "failures", maxConsecutiveFailures)
	}
}

// report emits the outcome of a successful batch.
func (e *Ext) report(batch string, resp common.EvaluationResponse, started time.Time, decisions []core.SystemOneDecision) {
	e.emit(core.SystemOneDecisionEvent{
		BaseEvent:   core.NewBaseEvent(core.EventSystemOneDecision, e.runner()),
		Batch:       batch,
		Model:       resp.Model,
		Duration:    time.Since(started),
		InputTokens: resp.Usage.InputTokens,
		Decisions:   decisions,
	})
}

func (e *Ext) emit(ev core.SystemOneDecisionEvent) {
	e.mu.Lock()
	em := e.emitter
	e.mu.Unlock()
	if em != nil {
		em.Emit(ev)
	}
}

func (e *Ext) runner() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runnerID
}
