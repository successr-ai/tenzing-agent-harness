package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tab58/huma-http-server/router"

	"github.com/successr-ai/tenzing-agent-harness/api/turnqueue"
	"github.com/successr-ai/tenzing-agent-harness/internal/adapters/eventbus"
	"github.com/successr-ai/tenzing-agent-harness/internal/app/nexus"
	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func query(srv *Server, q string) (*statusOutput, error) {
	in := &queryInput{}
	in.Body.Query = q
	return srv.handleQuery(context.Background(), router.MapAuthInfo{}, in)
}

func TestHandleQueryQueuesWhileBusyAndDrainsInOrder(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	srv := newTestServer(t, agent)

	if _, err := query(srv, "   "); err == nil {
		t.Fatal("blank query should be rejected")
	}
	out, err := query(srv, "q1")
	if err != nil || out.Body.Status != "started" {
		t.Fatalf("first query = (%+v, %v), want started", out, err)
	}
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })

	for _, q := range []string{"q2", "q3"} {
		if out, err := query(srv, q); err != nil || out.Body.Status != "queued" {
			t.Fatalf("%s = (%+v, %v), want queued", q, out, err)
		}
	}

	agent.gate <- struct{}{} // finish q1
	waitFor(t, "q2 to start from the queue", func() bool { return len(agent.seen()) == 2 })
	agent.gate <- struct{}{} // finish q2
	waitFor(t, "q3 to start from the queue", func() bool { return len(agent.seen()) == 3 })
	agent.gate <- struct{}{} // finish q3
	waitFor(t, "server to go idle", idle(srv))

	got := agent.seen()
	for i, want := range []string{"q1", "q2", "q3"} {
		if got[i] != want {
			t.Fatalf("processed order = %v", got)
		}
	}
}

func TestHandleQueryRejectedAfterShutdown(t *testing.T) {
	srv := newTestServer(t, &answerAgent{})
	srv.turns.Close()
	if _, err := query(srv, "q"); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("err = %v, want 409 shutting down", err)
	}
}

func TestHandleCancelDropsQueue(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	srv := newTestServer(t, agent)

	if _, err := srv.handleCancel(context.Background(), nil, nil); err == nil {
		t.Fatal("cancel with nothing running should 400")
	}
	query(srv, "q1")
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })
	query(srv, "q2")

	out, err := srv.handleCancel(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleCancel: %v", err)
	}
	if out.Body.Status != "cancelled (1 queued queries dropped)" {
		t.Fatalf("cancel status = %q", out.Body.Status)
	}
	waitFor(t, "server to go idle after cancel", idle(srv))
	if got := agent.seen(); len(got) != 1 {
		t.Fatalf("queries after cancel = %v, want only q1", got)
	}

	// a lone running turn reports a plain cancelled
	query(srv, "q3")
	waitFor(t, "q3", func() bool { return len(agent.seen()) == 2 })
	if out, _ := srv.handleCancel(context.Background(), nil, nil); out.Body.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", out.Body.Status)
	}
	waitFor(t, "idle", idle(srv))
}

func TestHandleSteer(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	srv := newTestServer(t, agent)
	steer := func(msg string) (*statusOutput, error) {
		in := &steerInput{}
		in.Body.Message = msg
		return srv.handleSteer(context.Background(), nil, in)
	}
	if _, err := steer("  "); err == nil {
		t.Error("blank steer should 400")
	}
	if _, err := steer("focus"); err == nil {
		t.Error("steer with nothing running should 400")
	}
	query(srv, "q1")
	waitFor(t, "q1", func() bool { return len(agent.seen()) == 1 })
	out, err := steer("focus")
	if err != nil || out.Body.Status != "steering" {
		t.Fatalf("steer while running = (%+v, %v)", out, err)
	}
	agent.gate <- struct{}{}
	waitFor(t, "idle", idle(srv))
}

func TestHandleStateAndInfo(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	srv := newTestServer(t, agent)
	srv.cfg.Cwd = "/work"

	out, err := srv.handleState(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleState: %v", err)
	}
	if out.Body.State != "idle" || out.Body.Queued != 0 || out.Body.Cwd != "/work" {
		t.Fatalf("idle state = %+v", out.Body)
	}
	if out.Body.ConversationID == "" || out.Body.Model == "" || out.Body.Tools == 0 || out.Body.ContextWindow != 131072 {
		t.Fatalf("state missing metadata: %+v", out.Body)
	}
	if out.Body.Vision {
		t.Fatalf("vision = true for non-vision test model")
	}
	info, err := srv.handleInfo(context.Background(), nil, nil)
	if err != nil || info.Body.Tools != out.Body.Tools {
		t.Fatalf("info = (%+v, %v)", info, err)
	}

	query(srv, "q1")
	waitFor(t, "q1 to reach the agent", func() bool { return len(agent.seen()) == 1 })
	query(srv, "q2")

	out, err = srv.handleState(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("handleState while running: %v", err)
	}
	if out.Body.State != "running" || out.Body.Queued != 1 {
		t.Fatalf("running state = %+v", out.Body)
	}
	agent.gate <- struct{}{}
	agent.gate <- struct{}{}
	waitFor(t, "idle", idle(srv))
}

func TestValidateImages(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	tests := []struct {
		name    string
		in      []imageInput
		wantErr string
	}{
		{"nil ok", nil, ""},
		{"valid", []imageInput{{MediaType: "image/png", Data: valid}}, ""},
		{"bad media type", []imageInput{{MediaType: "text/html", Data: valid}}, "not an image MIME type"},
		{"empty data", []imageInput{{MediaType: "image/png", Data: ""}}, "empty data"},
		{"bad base64", []imageInput{{MediaType: "image/png", Data: "not!!base64"}}, "not valid base64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := validateImages(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateImages: %v", err)
			}
			if len(out) != len(tt.in) {
				t.Errorf("got %d images, want %d", len(out), len(tt.in))
			}
		})
	}
}

// The test server's model has no vision support: image-bearing queries get a
// clean 400 before any turn starts; malformed images 400 even earlier.
func TestHandleQueryRejectsBadOrUnsupportedImages(t *testing.T) {
	agent := &gatedAgent{gate: make(chan struct{})}
	srv := newTestServer(t, agent)

	in := &queryInput{}
	in.Body.Query = "what is this?"
	in.Body.Images = []imageInput{{MediaType: "text/plain", Data: "x"}}
	if _, err := srv.handleQuery(context.Background(), router.MapAuthInfo{}, in); err == nil {
		t.Fatal("malformed image accepted")
	}
	in.Body.Images = []imageInput{{MediaType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("x"))}}
	_, err := srv.handleQuery(context.Background(), router.MapAuthInfo{}, in)
	if err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("err = %v, want vision-capability 400", err)
	}
	if len(agent.seen()) != 0 {
		t.Error("turn started despite capability rejection")
	}
}

// TestContextWindow proves the status gauge's denominator prefers the
// window the model actually runs at over its architectural maximum, and
// reports 0 (gauge hidden) when neither is known.
func TestContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		model common.Model
		want  int
	}{
		{"default window wins over maximum", common.ModelDefinition{ContextWindowSize: 200000, DefaultContextWindow: 32768}, 32768},
		{"maximum used when no default", common.ModelDefinition{ContextWindowSize: 200000}, 200000},
		{"unknown when neither set", common.ModelDefinition{}, 0},
		{"unknown when no model", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextWindow(tt.model); got != tt.want {
				t.Errorf("contextWindow() = %d, want %d", got, tt.want)
			}
		})
	}
}

// fakeNexus serves canned channel entries.
type fakeNexus struct {
	entries map[string][]nexus.Entry
}

func (f *fakeNexus) Read(name string, _ int, _ bool) ([]nexus.Entry, error) {
	e, ok := f.entries[name]
	if !ok {
		return nil, errors.New("no such channel")
	}
	return e, nil
}
func (f *fakeNexus) WebhookHandler() http.Handler { return http.NotFoundHandler() }

func TestStartNexusTurn(t *testing.T) {
	t.Run("no nexus configured", func(t *testing.T) {
		srv := newTestServer(t, &answerAgent{})
		if srv.StartNexusTurn([]string{"app"}) {
			t.Fatal("wake without a nexus should refuse")
		}
	})

	t.Run("starts a turn and emits nexus.trigger", func(t *testing.T) {
		agent := &gatedAgent{gate: make(chan struct{})}
		srv := newTestServer(t, agent)
		srv.cfg.Nexus = &fakeNexus{entries: map[string][]nexus.Entry{
			"app": {{Seq: 7, Text: "panic: nil deref"}},
		}}
		events := srv.cfg.Bus.(*eventbus.EventBus).Subscribe(16)

		if !srv.StartNexusTurn([]string{"app", "missing"}) {
			t.Fatal("wake on idle server should start a turn")
		}
		waitFor(t, "prompt to reach the agent", func() bool { return len(agent.seen()) == 1 })
		prompt := agent.seen()[0]
		for _, want := range []string{"channel(s) app, missing", `Recent errors from "app"`, "[7] panic: nil deref", "read_channel"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, `"missing"`) {
			t.Error("unreadable channel should be skipped, not listed")
		}
		waitFor(t, "nexus.trigger on the bus", func() bool {
			select {
			case ev := <-events:
				trig, ok := ev.(nexus.TriggerEvent)
				return ok && len(trig.Channels) == 2
			default:
				return false
			}
		})

		// busy: the wake is refused, never queued
		if srv.StartNexusTurn([]string{"app"}) {
			t.Error("wake while busy should refuse")
		}
		if _, queued := srv.turns.State(); queued != 0 {
			t.Error("wake queued a request")
		}
		agent.gate <- struct{}{}
		waitFor(t, "idle", idle(srv))
	})
}

var _ core.Event = nexus.TriggerEvent{}
var _ = turnqueue.Started
