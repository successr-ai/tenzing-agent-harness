package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/api/approvals"
	"github.com/successr-ai/tenzing-agent-harness/internal/app"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/wsclient"
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

// TestConnectSerialQueue pins the FIFO + flush semantics of the
// connect-mode turn queue.
func TestConnectSerialQueue(t *testing.T) {
	t.Run("second query waits for the first to finish", func(t *testing.T) {
		release := make(chan struct{})
		var mu sync.Mutex
		var order []string
		q := newConnectSerialQueue(func(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport {
			mu.Lock()
			order = append(order, cmd.ID)
			mu.Unlock()
			if cmd.ID == "q1" {
				<-release
			}
			return wsclient.TurnReport{Answer: cmd.ID}
		})

		go func() { _ = q.run(context.Background(), &wsclient.Query{ID: "q1"}) }()
		waitFor(t, "q1 running", func() bool { return q.parked() == 1 })
		done2 := make(chan struct{})
		go func() { _ = q.run(context.Background(), &wsclient.Query{ID: "q2"}); close(done2) }()
		waitFor(t, "q2 parked", func() bool { return q.parked() == 1 })

		close(release)
		waitFor(t, "both finished", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(order) == 2
		})
		mu.Lock()
		defer mu.Unlock()
		if order[0] != "q1" || order[1] != "q2" {
			t.Errorf("order = %v, want [q1 q2]", order)
		}
	})

	t.Run("flush makes waiters report cancelled", func(t *testing.T) {
		block := make(chan struct{})
		q := newConnectSerialQueue(func(ctx context.Context, cmd *wsclient.Query) wsclient.TurnReport {
			<-block
			return wsclient.TurnReport{}
		})
		done1 := make(chan struct{})
		go func() { _ = q.run(context.Background(), &wsclient.Query{ID: "q1"}); close(done1) }()
		// q1 must hold the slot before q2 is launched, or q2 may take it and
		// q1 becomes the flushed waiter — then done2 never closes.
		waitFor(t, "q1 running", func() bool { return q.parked() == 1 })
		done2 := make(chan struct{})
		var ctxErr error
		go func() {
			ctxErr = q.run(context.Background(), &wsclient.Query{ID: "q2"}).Err
			close(done2)
		}()

		// q2 is parked; flush it like a cancel command would.
		waitFor(t, "q2 parked", func() bool { return q.parked() == 1 })
		q.flush()
		<-done2
		if ctxErr == nil {
			t.Error("flushed waiter should report context.Canceled")
		}
		close(block)
		<-done1
	})
}
