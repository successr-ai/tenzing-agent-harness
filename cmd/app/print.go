package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wire"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

// jsonlWriter serializes concurrent JSON-line writes to one stream. Bus
// events and delta callbacks arrive from different goroutines. onErr, if
// set, fires once on the first failed write (e.g. the consumer closed the
// pipe) so the caller can abort the turn instead of streaming into a void.
type jsonlWriter struct {
	mu      sync.Mutex
	w       io.Writer
	onErr   func()
	errOnce sync.Once
}

func newJSONLWriter(w io.Writer) *jsonlWriter { return &jsonlWriter{w: w} }

func (j *jsonlWriter) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("jsonl marshal", "error", err)
		return
	}
	j.mu.Lock()
	_, werr := j.w.Write(append(b, '\n'))
	j.mu.Unlock()
	// onErr fires outside the mutex so a callback that (indirectly) writes
	// again can never self-deadlock; sync.Once makes the race-free "exactly
	// once" guarantee instead.
	if werr != nil {
		j.errOnce.Do(func() {
			slog.Warn("jsonl stdout write failed; aborting turn", "error", werr)
			if j.onErr != nil {
				j.onErr()
			}
		})
	}
}

// imageExts maps the file extensions accepted for @-image arguments to
// their MIME types.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// extractImageArgs splits "@screenshot.png"-style arguments from the query
// args. An @-arg with an image extension must be readable — a typo'd path
// errors rather than silently becoming query text. Other args (including
// @-args without an image extension) pass through.
func extractImageArgs(args []string) ([]string, []common.ImageSource, error) {
	var rest []string
	var images []common.ImageSource
	for _, a := range args {
		var mediaType string
		if strings.HasPrefix(a, "@") {
			mediaType = imageExts[strings.ToLower(filepath.Ext(a))]
		}
		if mediaType == "" {
			rest = append(rest, a)
			continue
		}
		data, err := os.ReadFile(strings.TrimPrefix(a, "@"))
		if err != nil {
			return nil, nil, fmt.Errorf("image argument %s: %w", a, err)
		}
		images = append(images, common.ImageSource{
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
		})
	}
	return rest, images, nil
}

// runPrint runs one headless agent turn. stdout carries only the answer
// (text mode) or JSONL events (json mode); a denied-tool summary goes to
// stderr; logs go to the log file. extraOpts is the test seam for stub
// LLM/brain injection.
func runPrint(ctx context.Context, cfg *cliConfig, stdout, stderr io.Writer, extraOpts ...harness.HarnessOption) error {
	model, err := cfg.deps.resolve(cfg.Model)
	if err != nil {
		return err
	}

	// "@path.png"-style tokens in the prompt attach image files to the turn.
	promptArgs, images, err := extractImageArgs(strings.Fields(cfg.Prompt))
	if err != nil {
		return err
	}
	query := strings.Join(promptArgs, " ")

	logFile, err := setupLogging(cfg.Debug, io.Discard)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Drop-in project/global config (F24/F22), gated by trust (F26). The
	// --trust flag grants for this run only; otherwise the persisted decision
	// or TENZING_PROJECT_TRUST applies. Project-config options come first so
	// an explicit --system (applied inside harnessOptions) wins over
	// override files.
	trusted := cfg.Trust
	if !trusted {
		if trustPath, err := app.TrustFilePath(); err == nil {
			trusted, _ = app.ResolveProjectTrust(trustPath, cwd, cfg.ProjectTrust)
		}
	}
	cfgDir, _ := os.UserConfigDir()
	pc := loadProjectConfig(cwd, cfgDir, trusted)
	pc.logDecisions()

	opts := pc.harnessOpts()
	cliOpts, err := harnessOptions(cfg)
	if err != nil {
		return err
	}
	opts = append(opts, cliOpts...)
	// Headless permission default: deny mutating tools instantly unless the
	// user explicitly set a timeout or disabled permissions.
	if !cfg.ApprovalTimeoutSet && !cfg.NoPermissions {
		opts = append(opts, harness.WithApprovalTimeout(0))
	}

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	var jw *jsonlWriter
	if cfg.OutputFormat == "json" {
		jw = newJSONLWriter(stdout)
		// A dead consumer (broken pipe) cancels the turn: no point burning
		// tokens streaming into a void.
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		jw.onErr = cancel
		opts = append(opts,
			harness.WithTextDeltaHandler(func(runnerID, text string) {
				jw.write(wire.TextDelta(runnerID, text))
			}),
			harness.WithThinkingDeltaHandler(func(runnerID, text string) {
				jw.write(wire.ThinkingDelta(runnerID, text))
			}),
		)
	}

	// The bus serves both output modes: json mode forwards every event to
	// stdout; both modes count permission denials so silent no-op runs (the
	// headless deny-instantly default) are visible on stderr.
	//
	// Delivery is lossless: an unbounded queue sits between the bus
	// subscription and the stdout writer. The drain goroutine never blocks
	// on stdout, so the 256-slot bus buffer can't fill up behind a slow
	// consumer and drop events (EventBus.Emit is drop-on-full by design —
	// the fix belongs here in the forwarder, not the bus).
	var (
		denied  atomic.Int64
		forward sync.WaitGroup
	)
	bus := eventbus.NewEventBus()
	evCh := bus.Subscribe(256)
	q := newEventQueue()
	forward.Add(2)
	go func() { // drain: bus → queue, never blocks on stdout
		defer forward.Done()
		for ev := range evCh {
			q.push(ev)
		}
		q.close()
	}()
	go func() { // write: queue → stdout, at consumer speed
		defer forward.Done()
		for {
			ev, ok := q.pop()
			if !ok {
				return
			}
			if _, ok := ev.(core.ToolDeniedEvent); ok {
				denied.Add(1)
			}
			if jw != nil {
				jw.write(wire.ToWire(ev))
			}
		}
	}()
	opts = append(opts, harness.WithEventBus(bus))

	opts = append(opts, extraOpts...)

	mainLLM, err := cfg.deps.llms.Get(model)
	if err != nil {
		return fmt.Errorf("harness init: %w", err)
	}
	h, err := harness.New(mainLLM, opts...)
	if err != nil {
		return fmt.Errorf("harness init: %w", err)
	}

	answer, turnErr := h.RunTurnWithImages(ctx, query, images)

	h.Shutdown()
	bus.Close()    // closes evCh, signals forward goroutine to finish
	forward.Wait() // wait for all events to be written

	deniedCount := int(denied.Load())
	if jw != nil {
		jw.write(wire.Result(answer, deniedCount, turnErr)) // after all harness events are flushed
	}
	if deniedCount > 0 {
		fmt.Fprintf(stderr, "%d tool call(s) denied by permission policy — pass --no-permissions or --approval-timeout to allow\n", deniedCount)
	}

	if turnErr != nil {
		return &exitCodeError{code: 2, err: fmt.Errorf("turn failed: %w", turnErr)}
	}
	if cfg.OutputFormat == "text" {
		fmt.Fprintln(stdout, answer)
	}
	return nil
}

// eventQueue is an unbounded FIFO decoupling bus delivery from stdout
// writes: push never blocks, pop blocks until an event arrives or the
// queue is closed and drained. Memory is bounded by one turn's event
// backlog. // ponytail: in-memory only; disk-spilling queue if RPC-mode
// turns ever produce more backlog than RAM comfortably holds.
type eventQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	events []core.Event
	closed bool
}

func newEventQueue() *eventQueue {
	q := &eventQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue) push(ev core.Event) {
	q.mu.Lock()
	q.events = append(q.events, ev)
	q.mu.Unlock()
	q.cond.Signal()
}

// close marks the queue complete: pending events still pop, then pop
// returns ok=false.
func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop blocks until an event is available or the queue is closed and fully
// drained. ok=false means no more events will ever arrive.
func (q *eventQueue) pop() (core.Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.events) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.events) == 0 {
		return nil, false
	}
	ev := q.events[0]
	q.events = q.events[1:]
	return ev, true
}

// logDir picks where log files go: <UserConfigDir>/tenzing/log (created on
// demand) rather than the cwd, so runs from arbitrary directories don't
// sprinkle log files wherever they run. Falls back to the OS temp dir when
// the config dir is unavailable.
func logDir() string {
	base := userConfigDir()
	if base == "" {
		return os.TempDir()
	}
	dir := filepath.Join(base, "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return os.TempDir()
	}
	return dir
}

// runPrintFn is the indirection RunE dispatches through, so tests can swap
// in a fake to observe the *cliConfig print mode receives. Package-global
// mutable state: tests that swap it must restore it and must not run in
// parallel with other cmd tests.
var runPrintFn = runPrint
