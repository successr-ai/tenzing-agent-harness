package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgfile "github.com/successr-ai/tenzing-agent-harness/internal/config"
	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	"github.com/successr-ai/tenzing-agent-harness/internal/core"
	"github.com/successr-ai/tenzing-agent-harness/internal/harness"
)

// Stubs mirror internal/harness/harness_test.go (test files aren't importable).

type stubAgent struct{}

func (s *stubAgent) GetCurrentModel() string               { return "stub-model" }
func (s *stubAgent) UpdateStreamCallback(_ func(string))   {}
func (s *stubAgent) UpdateThinkingCallback(_ func(string)) {}
func (s *stubAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	return core.ReasoningResult{FinalAnswer: "stub answer"}, nil
}

// stubFailingAgent mirrors stubAgent but fails the turn, for exercising
// runPrint's error path (exit code 2, JSON result line with is_error).
type stubFailingAgent struct{}

func (s *stubFailingAgent) GetCurrentModel() string               { return "stub-model" }
func (s *stubFailingAgent) UpdateStreamCallback(_ func(string))   {}
func (s *stubFailingAgent) UpdateThinkingCallback(_ func(string)) {}
func (s *stubFailingAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	return core.ReasoningResult{}, errors.New("stub reasoning failure")
}

// stubMutatingAgent issues one mutating tool call (bash), then finishes.
// Under print mode's deny-instantly permission default the call is denied,
// exercising the denied-tool summary paths.
type stubMutatingAgent struct{ called bool }

func (s *stubMutatingAgent) GetCurrentModel() string               { return "stub-model" }
func (s *stubMutatingAgent) UpdateStreamCallback(_ func(string))   {}
func (s *stubMutatingAgent) UpdateThinkingCallback(_ func(string)) {}
func (s *stubMutatingAgent) DoReasoning(_ context.Context, _ []common.Message, _ []string, _ []common.ToolDefinition) (core.ReasoningResult, error) {
	if !s.called {
		s.called = true
		return core.ReasoningResult{ToolCalls: []core.ToolCall{{ID: "c1", Name: "bash", Input: `{"command":"ls"}`}}}, nil
	}
	return core.ReasoningResult{FinalAnswer: "could not run the command"}, nil
}

type stubLLM struct{}

func (s *stubLLM) SendSyncMessage(_ context.Context, _ common.CompletionRequest) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) SendStreamingMessage(_ context.Context, _ common.CompletionRequest, _ chan<- common.StreamEvent) error {
	return nil
}
func (s *stubLLM) SendMessageWithTools(_ context.Context, _ common.CompletionRequest, _ []common.ToolDefinition) (common.CompletionResponse, error) {
	return common.CompletionResponse{}, nil
}
func (s *stubLLM) CountTokens(_ context.Context, _ common.CompletionRequest) (common.TokenCount, error) {
	return common.TokenCount{}, nil
}
func (s *stubLLM) ListModels(_ context.Context) ([]common.ModelInfo, error) { return nil, nil }
func (s *stubLLM) GetCurrentModel() string                                  { return "stub" }
func (s *stubLLM) GetContextWindowSize() int                                { return 4096 }
func (s *stubLLM) GetModel() common.Model {
	return common.ModelDefinition{Name: "stub", Provider: "ollama"}
}

func stubOpts() []harness.HarnessOption {
	return []harness.HarnessOption{
		harness.WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return &stubAgent{}, nil }),
	}
}

func failingOpts() []harness.HarnessOption {
	return []harness.HarnessOption{
		harness.WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return &stubFailingAgent{}, nil }),
	}
}

func printCfg(t *testing.T) *cliConfig {
	t.Helper()
	return &cliConfig{
		Prompt:       "hello",
		OutputFormat: "text",
		Model:        "test-model",
		deps:         printDeps(t),
	}
}

// printDeps builds a deps pair declaring the one model print tests resolve,
// "test-model", bound to the ollama test provider.
func printDeps(t *testing.T) *deps {
	t.Helper()
	d, err := buildDeps(
		[]cfgfile.Provider{{Name: "local", Type: "ollama", URL: "http://localhost:11434"}},
		cfgfile.ModelsSection{LLM: []cfgfile.ModelEntry{{Name: "test-model", Provider: "local", ModelName: "glm-5.3"}}},
		nil)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	return d
}

func TestRunPrintText(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // memory files land under a temp config dir
	var out bytes.Buffer
	if err := runPrint(context.Background(), printCfg(t), &out, io.Discard, stubOpts()...); err != nil {
		t.Fatalf("runPrint: %v", err)
	}
	if got := out.String(); got != "stub answer\n" {
		t.Errorf("stdout = %q, want %q", got, "stub answer\n")
	}
}

func TestRunPrintJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := printCfg(t)
	cfg.OutputFormat = "json"
	var out bytes.Buffer
	if err := runPrint(context.Background(), cfg, &out, io.Discard, stubOpts()...); err != nil {
		t.Fatalf("runPrint: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("no output lines")
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d not valid JSON: %s", i, line)
		}
	}
	var last struct {
		V    int    `json:"v"`
		Type string `json:"type"`
		Data struct {
			Answer  string `json:"answer"`
			IsError bool   `json:"is_error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("unmarshal result line: %v", err)
	}
	if last.V != 1 || last.Type != "result" || last.Data.Answer != "stub answer" || last.Data.IsError {
		t.Errorf("result line = %+v, want v=1 type=result answer=%q is_error=false", last, "stub answer")
	}
}

// TestRunPrintTurnFailure proves a failed turn surfaces as an
// *exitCodeError with code 2 (the code cobra's RunE relies on for the
// process exit status), not a plain error.
func TestRunPrintTurnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := runPrint(context.Background(), printCfg(t), &out, io.Discard, failingOpts()...)
	if err == nil {
		t.Fatal("runPrint: want error, got nil")
	}
	var exitErr *exitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runPrint error = %v (%T), want *exitCodeError", err, err)
	}
	if exitErr.code != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.code)
	}
}

// TestRunPrintJSONTurnFailure covers the json-mode counterpart: the final
// JSONL line must still be a well-formed result with is_error=true and an
// error message, even though the turn failed.
func TestRunPrintJSONTurnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := printCfg(t)
	cfg.OutputFormat = "json"
	var out bytes.Buffer
	err := runPrint(context.Background(), cfg, &out, io.Discard, failingOpts()...)

	var exitErr *exitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runPrint error = %v (%T), want *exitCodeError", err, err)
	}
	if exitErr.code != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.code)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("no output lines")
	}
	var last struct {
		Type string `json:"type"`
		Data struct {
			IsError bool   `json:"is_error"`
			Error   string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("unmarshal result line: %v", err)
	}
	if last.Type != "result" || !last.Data.IsError || last.Data.Error == "" {
		t.Errorf("result line = %+v, want type=result is_error=true with a non-empty error", last)
	}
}

func TestJSONLWriterSerializesConcurrently(t *testing.T) {
	var out bytes.Buffer
	w := newJSONLWriter(&out)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				w.write(map[string]int{"n": n, "j": j})
			}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	for i, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("interleaved write corrupted line %d: %s", i, line)
		}
	}
}

// TestRunPrintDeniedToolSummary proves a denied mutating tool call is
// visible: a stderr summary line and a denied_tools count on the JSON
// result line, so scripted runs can tell "nothing was actually done" from
// success.
func TestRunPrintDeniedToolSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := printCfg(t)
	cfg.OutputFormat = "json"
	opts := []harness.HarnessOption{
		harness.WithAgentBuilder(func(_ common.LLM, _ string) (core.Agent, error) { return &stubMutatingAgent{}, nil }),
	}
	var out, errBuf bytes.Buffer
	if err := runPrint(context.Background(), cfg, &out, &errBuf, opts...); err != nil {
		t.Fatalf("runPrint: %v", err)
	}

	if !strings.Contains(errBuf.String(), "denied by permission policy") {
		t.Errorf("stderr missing denial summary, got:\n%s", errBuf.String())
	}
	if !strings.Contains(out.String(), `"type":"tool.denied"`) {
		t.Errorf("stream missing tool.denied line, got:\n%s", out.String())
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var last struct {
		Type string `json:"type"`
		Data struct {
			DeniedTools int `json:"denied_tools"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("unmarshal result line: %v", err)
	}
	if last.Type != "result" || last.Data.DeniedTools != 1 {
		t.Errorf("result line = %+v, want type=result denied_tools=1", last)
	}
}

// TestEventQueueLossless proves the queue delivers every pushed event in
// order to a slow consumer — the lossless-delivery guarantee that replaced
// the old drop-on-full direct subscription — and that close() lets pending
// events drain before pop reports exhaustion.
func TestEventQueueLossless(t *testing.T) {
	const n = 1000 // well past the 256-slot bus buffer the queue protects
	q := newEventQueue()

	done := make(chan []core.Event)
	go func() {
		var got []core.Event
		for {
			ev, ok := q.pop()
			if !ok {
				done <- got
				return
			}
			got = append(got, ev)
		}
	}()

	sent := make([]core.Event, n)
	for i := range n {
		sent[i] = core.TurnStartedEvent{
			BaseEvent: core.NewBaseEvent(core.EventTurnStarted, "r1"),
			Query:     fmt.Sprintf("q%d", i),
		}
		q.push(sent[i])
	}
	q.close()

	got := <-done
	if len(got) != n {
		t.Fatalf("received %d events, want %d (lossless)", len(got), n)
	}
	for i := range n {
		if got[i].(core.TurnStartedEvent).Query != sent[i].(core.TurnStartedEvent).Query {
			t.Fatalf("event %d out of order: got %+v", i, got[i])
		}
	}
}

// TestEventQueueCloseWakesBlockedPop proves a pop blocked on an empty queue
// returns ok=false once close fires, instead of hanging forever.
func TestEventQueueCloseWakesBlockedPop(t *testing.T) {
	q := newEventQueue()
	done := make(chan bool)
	go func() {
		_, ok := q.pop()
		done <- ok
	}()
	q.close()
	if ok := <-done; ok {
		t.Error("pop after close on empty queue = ok=true, want false")
	}
}

// TestLogDir proves logs land in <UserConfigDir>/tenzing/log (created on
// demand), not the cwd.
func TestLogDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config") // linux; darwin ignores it

	dir := logDir()
	if filepath.Base(dir) != "log" || filepath.Base(filepath.Dir(dir)) != "tenzing" {
		t.Errorf("logDir() = %q, want a .../tenzing/log dir", dir)
	}
	if !strings.HasPrefix(dir, home) {
		t.Errorf("logDir() = %q, want under test home %q", dir, home)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("logDir() did not create %q: %v", dir, err)
	}
}

type failingWriter struct{ err error }

func (f *failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

// TestJSONLWriterFiresOnErrOnce proves a failed write triggers onErr exactly
// once (the dead-consumer abort hook), no matter how many writes follow.
func TestJSONLWriterFiresOnErrOnce(t *testing.T) {
	calls := 0
	w := newJSONLWriter(&failingWriter{err: errors.New("broken pipe")})
	w.onErr = func() { calls++ }
	w.write(map[string]string{"n": "1"})
	w.write(map[string]string{"n": "2"})
	if calls != 1 {
		t.Errorf("onErr fired %d times, want exactly 1", calls)
	}
}
