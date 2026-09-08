package blackboard

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
)

// Caps for blackboard log attributes, so one exec can't flood the log.
const (
	logCodeMax   = 500
	logStdoutMax = 200
)

func capStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// setupCode runs once when the REPL starts: the shared namespace plus
// inspection helpers available to every agent. `re` is already in the
// bootstrap namespace.
// bb enforces slot ownership on writes: each Execute sets _BB._writer to the
// calling agent's ID, and creating/replacing a top-level key that isn't your
// own slot raises PermissionError. Reads are unrestricted. This is
// anti-confusion, not security — agents share one interpreter.
// ponytail: top-level keys only; deep guards if cross-slot mutation ever bites.
const setupCode = `
class _BB(dict):
    _writer = None
    def _check(self, k):
        if _BB._writer is not None and k != _BB._writer:
            raise PermissionError(
                "bb[%r] is not your slot; deposit results only in bb[%r]" % (k, _BB._writer))
    def __setitem__(self, k, v):
        self._check(k)
        dict.__setitem__(self, k, v)
    def __delitem__(self, k):
        self._check(k)
        dict.__delitem__(self, k)
    def setdefault(self, k, default=None):
        if k not in self:
            self._check(k)
        return dict.setdefault(self, k, default)

bb = _BB()
bb['main'] = {}

def peek(s, start=0, n=2000):
    s = s if isinstance(s, str) else str(s)
    end = min(start + n, len(s))
    return "[chars %d-%d of %d]\n" % (start, end, len(s)) + s[start:end]

def bb_grep(pattern, s, max_matches=100):
    s = s if isinstance(s, str) else str(s)
    out = []
    for i, line in enumerate(s.split("\n")):
        if re.search(pattern, line):
            out.append("%d:%s" % (i, line))
            if len(out) >= max_matches:
                out.append("[capped at %d matches]" % max_matches)
                break
    return "\n".join(out)
`

// Config configures a Blackboard.
type Config struct {
	// Querier powers llm_query/llm_batch inside the REPL. Nil leaves them
	// erroring at call time.
	Querier Querier
	// WorkingDir is the REPL subprocess working directory.
	WorkingDir string
	// HeadChars/TailChars are preview sizes; zero means the defaults.
	HeadChars int
	TailChars int
}

// Blackboard is a persistent shared Python REPL. It is strictly an
// agent→subagent communication mechanism: one-shot subagents deposit their
// findings here before disconnecting, and the parent inspects them. It is
// not designed for long-lived agents to share results — contents are
// in-memory only and lost on any transport failure or reset. All access is
// serialized by mu — that single mutex is the entire concurrency contract.
// The Python process starts lazily on first use.
type Blackboard struct {
	cfg Config

	mu      sync.Mutex
	repl    *REPL
	started bool
	closed  bool
}

// New creates a Blackboard. The Python subprocess is not started until the
// first Execute or Deposit call.
func New(cfg Config) *Blackboard {
	if cfg.HeadChars <= 0 {
		cfg.HeadChars = DefaultHeadChars
	}
	if cfg.TailChars <= 0 {
		cfg.TailChars = DefaultTailChars
	}
	return &Blackboard{cfg: cfg}
}

// HeadChars returns the resolved preview head size.
func (b *Blackboard) HeadChars() int { return b.cfg.HeadChars }

// TailChars returns the resolved preview tail size.
func (b *Blackboard) TailChars() int { return b.cfg.TailChars }

// ensureStartedLocked lazily boots the Python process. Callers must hold mu.
func (b *Blackboard) ensureStartedLocked(ctx context.Context) error {
	if b.closed {
		return fmt.Errorf("blackboard is closed")
	}
	if b.started {
		return nil
	}
	repl, err := NewREPL(b.cfg.Querier, b.cfg.WorkingDir)
	if err != nil {
		return fmt.Errorf("start blackboard repl: %w", err)
	}
	if _, _, _, err := repl.Execute(ctx, setupCode); err != nil {
		_ = repl.Close()
		return fmt.Errorf("blackboard setup: %w", err)
	}
	b.repl = repl
	b.started = true
	return nil
}

// resetLocked tears down a broken REPL so the next call restarts fresh.
// Callers must hold mu. Blackboard contents are lost.
func (b *Blackboard) resetLocked() {
	if b.repl != nil {
		_ = b.repl.Close()
	}
	b.repl = nil
	b.started = false
}

// Execute runs code in the shared namespace on behalf of agentID and
// returns captured stdout. Python exceptions come back in stdout (as
// "[Python Error] ..."), not as Go errors; a Go error means the REPL
// transport failed and the blackboard was reset.
func (b *Blackboard) Execute(ctx context.Context, agentID, code string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureStartedLocked(ctx); err != nil {
		return "", err
	}
	// Arm the slot-ownership guard for this caller. agentID is validated so
	// it can be spliced into Python safely.
	if !slotKeyRe.MatchString(agentID) {
		return "", fmt.Errorf("invalid blackboard agent id %q: must match [A-Za-z0-9_]+", agentID)
	}
	code = fmt.Sprintf("_BB._writer = %q\n", agentID) + code
	stdout, _, _, err := b.repl.Execute(ctx, code)
	if err != nil {
		slog.Error("[blackboard] exec failed", "agent", agentID, "code", capStr(code, logCodeMax), "error", err)
		b.resetLocked()
		return "", fmt.Errorf("blackboard execute for %s failed (repl restarted, bb contents lost): %w", agentID, err)
	}
	slog.Debug("[blackboard] exec", "agent", agentID, "code", capStr(code, logCodeMax),
		"stdout_len", len(stdout), "stdout_head", capStr(stdout, logStdoutMax))
	return stdout, nil
}

// Close shuts down the Python subprocess and marks the Blackboard closed so
// any later Execute/Deposit call fails instead of lazily restarting python.
// Safe to call before first use.
func (b *Blackboard) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if !b.started {
		return nil
	}
	err := b.repl.Close()
	b.repl = nil
	b.started = false
	return err
}

// slotKeyRe restricts blackboard slot/key names so they can be spliced into
// generated Python without escaping or injection.
var slotKeyRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Deposit stores value at bb[slot][key] and returns its Preview. The value
// itself travels via SetVar (JSON-encoded), never through generated code.
func (b *Blackboard) Deposit(ctx context.Context, slot, key, value string) (Preview, error) {
	if !slotKeyRe.MatchString(slot) {
		return Preview{}, fmt.Errorf("invalid blackboard slot %q: must match [A-Za-z0-9_]+", slot)
	}
	if !slotKeyRe.MatchString(key) {
		return Preview{}, fmt.Errorf("invalid blackboard key %q: must match [A-Za-z0-9_]+", key)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureStartedLocked(ctx); err != nil {
		return Preview{}, err
	}
	if err := b.repl.SetVar("_bb_deposit", value); err != nil {
		b.resetLocked()
		return Preview{}, fmt.Errorf("blackboard deposit for %s: %w", slot, err)
	}
	// dict.setdefault bypasses the _BB write guard: Deposit is the trusted
	// Go path and writes into whatever slot the harness names.
	code := fmt.Sprintf("dict.setdefault(bb, '%s', {})['%s'] = _bb_deposit\ndel _bb_deposit", slot, key)
	if _, _, _, err := b.repl.Execute(ctx, code); err != nil {
		b.resetLocked()
		return Preview{}, fmt.Errorf("blackboard deposit for %s: %w", slot, err)
	}
	slog.Debug("[blackboard] deposit", "slot", slot, "key", key, "value_len", len(value))
	return NewPreview(slot, key, value, b.cfg.HeadChars, b.cfg.TailChars), nil
}
