package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// testModel stands in for a caller-supplied model; NewClient no longer has a
// default.
var testModel Model = common.ModelDefinition{
	Name:                 "gemma4:31b",
	MaxTokens:            32_768,
	ContextWindowSize:    262_144,
	DefaultContextWindow: 32_768,
	Provider:             "ollama",
}

// mustNewClient builds a client with the test model, failing the test on
// construction errors.
func mustNewClient(t *testing.T, opts ...ClientOption) common.LLM {
	t.Helper()
	client, err := NewClient(testModel, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func newOllamaTestClient(t *testing.T, handler http.HandlerFunc) common.LLM {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return mustNewClient(t, WithBaseURL(srv.URL))
}

func TestOllama_SendSyncMessage(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"model": "test-model",
			"message": {"role": "assistant", "content": "<think>hmm</think>hello"},
			"done": true, "done_reason": "stop",
			"prompt_eval_count": 8, "eval_count": 4
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	if res.Text() != "hello" {
		t.Errorf("text = %q, want hello (think block stripped)", res.Text())
	}
	if res.StopReason != common.StopReasonStop {
		t.Errorf("stop reason = %q, want %q", res.StopReason, common.StopReasonStop)
	}
	if res.Usage.InputTokens != 8 || res.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v, want 8/4", res.Usage)
	}
}

func TestOllama_SendMessageWithTools(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"model": "test-model",
			"message": {"role": "assistant", "content": "", "tool_calls": [
				{"function": {"name": "read_file", "arguments": {"path": "main.go"}}}
			]},
			"done": true, "done_reason": "stop",
			"prompt_eval_count": 8, "eval_count": 4
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := client.SendMessageWithTools(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("read main.go")},
	}, []common.ToolDefinition{{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}})
	if err != nil {
		t.Fatalf("SendMessageWithTools: %v", err)
	}

	calls := res.ToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "read_file" {
		t.Fatalf("tool calls = %+v, want one read_file call", calls)
	}
	if res.StopReason != common.StopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", res.StopReason, common.StopReasonToolUse)
	}
}

func TestOllama_ErrorIncludesResponseBody(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error": "model not found"}`, http.StatusNotFound)
	})

	_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "missing-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error should include status and body: %v", err)
	}
}

func TestOllama_ListModels(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"models": [
			{"name": "qwen3.5:9b", "model": "qwen3.5:9b"},
			{"name": "gemma4:31b", "model": "gemma4:31b"}
		]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "qwen3.5:9b" || models[1].ID != "gemma4:31b" {
		t.Errorf("models = %+v, want qwen3.5:9b and gemma4:31b", models)
	}
}

func TestOllama_CountTokensNotSupported(t *testing.T) {
	client := mustNewClient(t)
	_, err := client.CountTokens(context.Background(), common.CompletionRequest{})
	if !errors.Is(err, common.ErrNotSupported) {
		t.Fatalf("want ErrNotSupported, got %v", err)
	}
}

func TestOllama_GetCurrentModel(t *testing.T) {
	client := mustNewClient(t)
	if got := client.GetCurrentModel(); got != testModel.GetName() {
		t.Errorf("GetCurrentModel() = %q, want %q", got, testModel.GetName())
	}
}

func TestOllama_NewClientRequiresModel(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("NewClient without Model: want error, got nil")
	}
}

// The request's Think field must pass through to the wire verbatim: nil omits
// `think` entirely (model default — thinking models think, non-thinking models
// don't error), explicit true/false are sent as-is. Ollama rejects think:true
// for models without thinking support, so nil must never be coerced to a bool.
func TestOllama_ThinkRequestPassthrough(t *testing.T) {
	tests := []struct {
		name  string
		think *bool
		want  string // raw JSON expectation about the "think" key
	}{
		{"nil omits think", nil, "absent"},
		{"explicit true", boolPtr(true), "true"},
		{"explicit false", boolPtr(false), "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			client := newOllamaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`))
			})

			_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:    "m",
				Messages: []common.Message{common.NewUserMessage("hi")},
				Think:    tt.think,
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			got, present := body["think"]
			switch tt.want {
			case "absent":
				if present {
					t.Errorf("request sent think=%v, want key absent", got)
				}
			case "true", "false":
				if !present || fmt.Sprintf("%v", got) != tt.want {
					t.Errorf("request think = %v (present=%v), want %s", got, present, tt.want)
				}
			}
		})
	}
}

// Thinking models on Ollama Cloud return chain-of-thought in the message's
// native `thinking` field. It must surface as a thinking block, not vanish
// and not pollute Text().
func TestOllama_SendSyncMessage_NativeThinkingField(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"model": "glm-5.2:cloud",
			"message": {"role": "assistant", "content": "hello", "thinking": "pondering"},
			"done": true, "done_reason": "stop"
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "glm-5.2:cloud",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}
	if res.Text() != "hello" {
		t.Errorf("Text() = %q, want hello", res.Text())
	}
	if res.Thinking() != "pondering" {
		t.Errorf("Thinking() = %q, want pondering", res.Thinking())
	}
}

// Streaming chunks can carry native `thinking` deltas alongside content
// deltas. They must be emitted as StreamEventThinking and accumulated into a
// thinking block on the terminal response.
func TestOllama_SendStreamingMessage_NativeThinkingDeltas(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		chunks := []string{
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","thinking":"hmm, "},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","thinking":"tricky"},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":"answer"},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n"))
		}
	})

	events := make(chan common.StreamEvent, 64)
	go func() {
		if err := client.SendStreamingMessage(context.Background(), common.CompletionRequest{}, events); err != nil {
			t.Errorf("SendStreamingMessage: %v", err)
		}
	}()

	var thinkingDeltas, textDeltas string
	var stop *common.CompletionResponse
	for ev := range events {
		switch ev.Type {
		case common.StreamEventThinking:
			thinkingDeltas += ev.Text
		case common.StreamEventDelta:
			textDeltas += ev.Text
		case common.StreamEventStop:
			stop = ev.Response
		}
	}
	if thinkingDeltas != "hmm, tricky" {
		t.Errorf("thinking deltas = %q, want %q", thinkingDeltas, "hmm, tricky")
	}
	if textDeltas != "answer" {
		t.Errorf("text deltas = %q, want %q", textDeltas, "answer")
	}
	if stop == nil {
		t.Fatal("no StreamEventStop received")
	}
	if stop.Thinking() != "hmm, tricky" {
		t.Errorf("final response Thinking() = %q, want %q", stop.Thinking(), "hmm, tricky")
	}
	if stop.Text() != "answer" {
		t.Errorf("final response Text() = %q, want %q", stop.Text(), "answer")
	}
}

// Ollama streams assistant text spread across many chunks; the final done
// chunk usually carries empty content. The terminal StreamEventStop response
// must contain the full accumulated text, or agent loops see an empty final
// answer (and retry pointlessly) whenever the model streams.
func TestOllama_SendStreamingMessage_AccumulatesTextAcrossChunks(t *testing.T) {
	client := newOllamaTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		chunks := []string{
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":"The root "},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":"cause is a "},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":"deadlock."},"done":false}`,
			`{"model":"glm-5.2:cloud","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","eval_count":12}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n"))
		}
	})

	events := make(chan common.StreamEvent, 64)
	go func() {
		if err := client.SendStreamingMessage(context.Background(), common.CompletionRequest{}, events); err != nil {
			t.Errorf("SendStreamingMessage: %v", err)
		}
	}()

	var stop *common.CompletionResponse
	for ev := range events {
		if ev.Type == common.StreamEventStop {
			stop = ev.Response
		}
	}
	if stop == nil {
		t.Fatal("no StreamEventStop received")
	}
	var text string
	for _, block := range stop.Content {
		if block.Type == common.ContentTypeText {
			text += block.Text
		}
	}
	if text != "The root cause is a deadlock." {
		t.Errorf("final response text = %q, want full accumulated text", text)
	}
}
