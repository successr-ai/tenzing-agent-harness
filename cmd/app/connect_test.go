package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/features/permissions"
)

// TestConnectModeExclusiveWithPrompt: --connect and -p cannot combine.
func TestConnectModeExclusiveWithPrompt(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"-p", "hi", "--connect", "ws://localhost:1/fleet"})
	if err := cmd.Execute(); err == nil {
		t.Error("want error for --connect with -p, got nil")
	}
}

// TestConnectConfigMerge: tenzing.yaml's connect: section fills the config
// under flag/env precedence.
func TestConnectConfigMerge(t *testing.T) {
	changedNone := func(string) bool { return false }
	presentNone := func(string) bool { return false }

	t.Run("file fills unset fields", func(t *testing.T) {
		cfg := &cliConfig{}
		mergeConfigFile(cfg, cfgfile.File{Connect: &cfgfile.ConnectSection{
			URL:   "ws://plane:9000/fleet",
			Token: "secret",
		}}, changedNone, presentNone)
		if cfg.ConnectURL != "ws://plane:9000/fleet" || cfg.ConnectToken != "secret" {
			t.Errorf("connect not merged: %+v", cfg)
		}
	})

	t.Run("changed flag beats file", func(t *testing.T) {
		cfg := &cliConfig{ConnectURL: "ws://flag"}
		mergeConfigFile(cfg, cfgfile.File{Connect: &cfgfile.ConnectSection{URL: "ws://file"}},
			func(name string) bool { return name == "connect" }, presentNone)
		if cfg.ConnectURL != "ws://flag" {
			t.Errorf("flag lost to file: %q", cfg.ConnectURL)
		}
	})

	t.Run("ephemeral_grants false carries", func(t *testing.T) {
		cfg := &cliConfig{ConnectEphemeralGrants: true} // the flag default
		f := false
		mergeConfigFile(cfg, cfgfile.File{Connect: &cfgfile.ConnectSection{EphemeralGrants: &f}},
			changedNone, presentNone)
		if cfg.ConnectEphemeralGrants {
			t.Error("ephemeral_grants: false not merged")
		}
	})

	t.Run("ephemeral_grants loses to env", func(t *testing.T) {
		cfg := &cliConfig{ConnectEphemeralGrants: true}
		f := false
		mergeConfigFile(cfg, cfgfile.File{Connect: &cfgfile.ConnectSection{EphemeralGrants: &f}},
			changedNone, func(name string) bool { return name == "TENZING_CONNECT_EPHEMERAL_GRANTS" })
		if !cfg.ConnectEphemeralGrants {
			t.Error("file beat a present env var")
		}
	})

	t.Run("backoff duration carries", func(t *testing.T) {
		cfg := &cliConfig{}
		d := cfgfile.Duration(5 * time.Second)
		mergeConfigFile(cfg, cfgfile.File{Connect: &cfgfile.ConnectSection{Backoff: &d}},
			changedNone, presentNone)
		if cfg.ConnectBackoff != 5*time.Second {
			t.Errorf("backoff = %v, want 5s", cfg.ConnectBackoff)
		}
	})
}

// TestMergeEnvConnect: TENZING_CONNECT / TENZING_CONNECT_TOKEN fill flag
// defaults.
func TestMergeEnvConnect(t *testing.T) {
	t.Setenv("TENZING_CONNECT", "ws://env-plane/fleet")
	t.Setenv("TENZING_CONNECT_TOKEN", "env-token")
	t.Setenv("TENZING_CONNECT_EPHEMERAL_GRANTS", "false")

	cfg := &cliConfig{ConnectEphemeralGrants: true}
	mergeEnv(cfg, &Config{}, func(string) bool { return false }, func(string) bool { return true })
	if cfg.ConnectURL != "ws://env-plane/fleet" || cfg.ConnectToken != "env-token" {
		t.Errorf("env vars not merged: url=%q token=%q", cfg.ConnectURL, cfg.ConnectToken)
	}
	if cfg.ConnectEphemeralGrants {
		t.Error("TENZING_CONNECT_EPHEMERAL_GRANTS=false not merged")
	}
}

// TestConnectApprove pins the grant policy behind approve.glob: ephemeral
// grants live in the rules only; durable ones also reach the settings file.
func TestConnectApprove(t *testing.T) {
	tests := []struct {
		name      string
		ephemeral bool
		approved  bool
		glob      string
		wantRule  bool
		wantFile  bool
	}{
		{"ephemeral grant stays in memory", true, true, "go test *", true, false},
		{"durable grant reaches settings.json", false, true, "go test *", true, true},
		{"denied with glob adds nothing", true, false, "go test *", false, false},
		{"approved without glob adds nothing", false, true, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			rules := permissions.NewBashRules(nil, nil)
			store := app.NewBashAllowStore(path, rules)
			registry := approvals.NewRegistry()
			var answered *bool
			registry.Add("call-1", approvals.Pending{Respond: func(ok bool) { answered = &ok }})

			connectApprove(store, registry, tt.ephemeral, "call-1", tt.approved, tt.glob)

			if answered == nil || *answered != tt.approved {
				t.Fatalf("approval answered = %v, want %v", answered, tt.approved)
			}
			allow, _ := rules.Lists()
			allow = append(allow, rules.SessionList()...)
			if got := slices.Contains(allow, tt.glob) && tt.glob != ""; got != tt.wantRule {
				t.Errorf("rule present = %v, want %v (allow=%v)", got, tt.wantRule, allow)
			}
			if persisted, _ := rules.Lists(); tt.ephemeral && len(persisted) != 0 {
				t.Errorf("ephemeral grant reached the persisted list: %v", persisted)
			}
			_, err := os.Stat(path)
			if got := err == nil; got != tt.wantFile {
				t.Errorf("settings file exists = %v, want %v", got, tt.wantFile)
			}
		})
	}
}

// TestObserveConnectEventFilesTouched: successful Read/Edit/Write calls
// record their path once; other tools, failures, and denials do not.
func TestObserveConnectEventFilesTouched(t *testing.T) {
	stats := newTurnStats()
	registry := approvals.NewRegistry()
	subagents := map[string]string{}
	feed := func(ev core.Event) { observeConnectEvent(ev, registry, subagents, stats) }

	feed(core.ToolSucceededEvent{ToolName: "Read", Input: `{"file_path":"a.go"}`})
	feed(core.ToolSucceededEvent{ToolName: "Write", Input: `{"file_path":"a.go","content":"x"}`})
	feed(core.ToolSucceededEvent{ToolName: "Edit", Input: `{"file_path":"b.go"}`})
	feed(core.ToolSucceededEvent{ToolName: "Bash", Input: `{"command":"touch c.go"}`})
	feed(core.ToolFailedEvent{ToolName: "Write", Input: `{"file_path":"d.go"}`})
	feed(core.ToolDeniedEvent{ToolName: "Write", Input: `{"file_path":"e.go"}`})
	feed(core.ToolSucceededEvent{ToolName: "Read", Input: `not json`})

	denied, files := stats.snapshot()
	if want := []string{"a.go", "b.go"}; !slices.Equal(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
	if denied != 1 {
		t.Errorf("denied = %d, want 1", denied)
	}

	stats.reset()
	if d, f := stats.snapshot(); d != 0 || len(f) != 0 {
		t.Errorf("after reset: denied=%d files=%v, want empty", d, f)
	}
}

// fakeConnectSink records the envelope types forwarded for a running turn.
// A non-nil gate stalls every SendEvent until it closes (a slow plane).
type fakeConnectSink struct {
	gate        chan struct{}
	mu          sync.Mutex
	types       []string
	disconnects int
	// sending counts SendEvent calls entered, stalled or not.
	sending int
}

func (s *fakeConnectSink) Running() string                   { return "q1" }
func (s *fakeConnectSink) ConnContext() context.Context      { return context.Background() }
func (s *fakeConnectSink) RequestApproval(_, _, _, _ string) {}
func (s *fakeConnectSink) Disconnect(string) {
	s.mu.Lock()
	s.disconnects++
	s.mu.Unlock()
}
func (s *fakeConnectSink) SendEvent(_ context.Context, _ string, envelope json.RawMessage) {
	var env struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(envelope, &env)
	s.mu.Lock()
	s.sending++
	s.mu.Unlock()
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	s.types = append(s.types, env.Type)
	s.mu.Unlock()
}

// TestForwardConnectEventsFlushDrainsBeforeAck: a flush forwards and
// tallies everything already queued before it acks, so the turn's result
// can follow its events.
func TestForwardConnectEventsFlushDrainsBeforeAck(t *testing.T) {
	ch := make(chan core.Event, 16)
	ch <- core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, ""), Query: "hi"}
	ch <- core.ToolDeniedEvent{BaseEvent: core.NewBaseEvent(core.EventToolDenied, ""), ToolName: "Write"}
	ch <- core.LoopStoppedEvent{BaseEvent: core.NewBaseEvent(core.EventLoopStopped, "")}
	ch <- core.TurnCompletedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnCompleted, ""), FinalAnswer: "done"}

	sink := &fakeConnectSink{}
	stats := newTurnStats()
	flush := make(chan chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardConnectEvents(ctx, ch, sink, approvals.NewRegistry(), stats, flush)

	ack := make(chan struct{})
	flush <- ack
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("flush never acked")
	}

	sink.mu.Lock()
	got := append([]string(nil), sink.types...)
	sink.mu.Unlock()
	want := []string{"turn.started", "tool.denied", "loop.stopped", "turn.completed"}
	if !slices.Equal(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
	if denied, _ := stats.snapshot(); denied != 1 {
		t.Errorf("denied = %d, want 1", denied)
	}
}

// TestForwardConnectEventsSkipsSettingsEvents: model.changed and
// thinking.changed echo plane commands between turns — not turn events —
// so they never go upstream, where they'd be tagged with whatever turn
// happens to be running when forwarded.
func TestForwardConnectEventsSkipsSettingsEvents(t *testing.T) {
	ch := make(chan core.Event, 16)
	ch <- core.ModelChangedEvent{BaseEvent: core.NewBaseEvent(core.EventModelChanged, ""), From: "a", To: "b"}
	ch <- core.ThinkingChangedEvent{BaseEvent: core.NewBaseEvent(core.EventThinkingChanged, ""), Enabled: true}
	ch <- core.TurnStartedEvent{BaseEvent: core.NewBaseEvent(core.EventTurnStarted, ""), Query: "hi"}

	sink := &fakeConnectSink{}
	flush := make(chan chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardConnectEvents(ctx, ch, sink, approvals.NewRegistry(), newTurnStats(), flush)

	ack := make(chan struct{})
	flush <- ack
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("flush never acked")
	}
	sink.mu.Lock()
	got := append([]string(nil), sink.types...)
	sink.mu.Unlock()
	if want := []string{"turn.started"}; !slices.Equal(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
}

// TestForwardConnectEventsSlowPlaneNoDrops: a stalled plane must not back
// the bus subscription up into EventBus.Emit's drop-on-full path; every
// event is still forwarded, in order, once the plane catches up.
func TestForwardConnectEventsSlowPlaneNoDrops(t *testing.T) {
	bus := eventbus.NewEventBus()
	ch := bus.Subscribe(16)
	sink := &fakeConnectSink{gate: make(chan struct{})}
	flush := make(chan chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardConnectEvents(ctx, ch, sink, approvals.NewRegistry(), newTurnStats(), flush)

	const n = 500
	for i := range n {
		bus.Emit(core.ToolDeniedEvent{BaseEvent: core.NewBaseEvent(core.EventToolDenied, ""), ToolName: "Write"})
		if i%8 == 7 { // stay under the 16-slot buffer: only a stalled drain can overflow it
			waitFor(t, "subscription drained while the plane is stalled", func() bool { return len(ch) == 0 })
		}
	}
	close(sink.gate)

	ack := make(chan struct{})
	flush <- ack
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("flush never acked")
	}
	sink.mu.Lock()
	got := len(sink.types)
	sink.mu.Unlock()
	if got != n {
		t.Errorf("forwarded %d events, want %d", got, n)
	}
}

// TestForwardConnectEventsOverflowDisconnects: a backlog past the cap
// means the plane has stopped reading. The forwarder discards the backlog
// and drops the connection, and a flush caught in the discarded backlog
// still acks, so the waiting turn can't hang.
func TestForwardConnectEventsOverflowDisconnects(t *testing.T) {
	defer func(n int) { connectBacklogCap = n }(connectBacklogCap)
	connectBacklogCap = 10

	ch := make(chan core.Event, 64)
	ev := core.ToolDeniedEvent{BaseEvent: core.NewBaseEvent(core.EventToolDenied, ""), ToolName: "Write"}
	sink := &fakeConnectSink{gate: make(chan struct{})}
	defer close(sink.gate)
	flush := make(chan chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forwardConnectEvents(ctx, ch, sink, approvals.NewRegistry(), newTurnStats(), flush)

	ch <- ev // the forwarder takes it and stalls in SendEvent
	// Wait for the stall: an event still queued would leave 21 items, which
	// overflows the cap twice and disconnects twice.
	waitFor(t, "stalled send", func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return sink.sending == 1
	})
	ack := make(chan struct{})
	flush <- ack // queued behind the stalled send
	for range 20 {
		ch <- ev
	}
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("flush in the discarded backlog never acked")
	}
	waitFor(t, "disconnect", func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return sink.disconnects == 1
	})
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
