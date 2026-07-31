package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// testModel stands in for a caller-supplied model; NewClient no longer has a
// default.
var testModel Model = common.ModelDefinition{
	Name:              "claude-sonnet-4-6",
	MaxTokens:         128_000,
	ContextWindowSize: 1_000_000,
	Provider:          "anthropic",
}

// newAnthropicTestClient creates an Anthropic client pointed at a test server.
// Config has no BaseURL override (the Anthropic endpoint is fixed), so the
// client is constructed directly. No rate limiting, so requests don't trigger
// CountTokens API calls.
func newAnthropicTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	sdkClient := anthropicSDK.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
	)
	return &Client{client: &sdkClient, model: testModel}
}

func writeSSE(t *testing.T, w http.ResponseWriter, eventType, data string) {
	t.Helper()
	if _, err := w.Write([]byte("event: " + eventType + "\ndata: " + data + "\n\n")); err != nil {
		t.Errorf("write SSE event: %v", err)
	}
}

func TestAnthropic_StreamingAccumulatesContent(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`)
		writeSSE(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`)
		writeSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`)
		writeSSE(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeSSE(t, w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`)
		writeSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
		writeSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`)
		writeSSE(t, w, "content_block_stop", `{"type":"content_block_stop","index":1}`)
		writeSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":25}}`)
		writeSSE(t, w, "message_stop", `{"type":"message_stop"}`)
	})

	events := make(chan common.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.SendStreamingMessage(context.Background(), common.CompletionRequest{
			Model:    "claude-sonnet-4-6",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}, events)
	}()

	deltas, response := collectEvents(t, events)
	if err := <-errCh; err != nil {
		t.Fatalf("SendStreamingMessage: %v", err)
	}

	if got := strings.Join(deltas, ""); got != "hello" {
		t.Errorf("joined deltas = %q, want hello", got)
	}
	if response.Text() != "hello" {
		t.Errorf("accumulated text = %q, want hello", response.Text())
	}

	calls := response.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1; content: %+v", len(calls), response.Content)
	}
	if calls[0].ToolUseID != "toolu_1" || calls[0].ToolName != "read_file" {
		t.Errorf("tool call = %s/%s, want toolu_1/read_file", calls[0].ToolUseID, calls[0].ToolName)
	}
	if string(calls[0].ToolInput) != `{"path":"main.go"}` {
		t.Errorf("tool input = %s, want fully assembled JSON", calls[0].ToolInput)
	}

	if response.StopReason != common.StopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", response.StopReason, common.StopReasonToolUse)
	}
	if response.Usage.InputTokens != 10 || response.Usage.OutputTokens != 25 {
		t.Errorf("usage = %+v, want 10/25", response.Usage)
	}
}

func TestAnthropic_SendMessageWithTools(t *testing.T) {
	var requestBody []byte
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
			"content": [
				{"type": "text", "text": "reading"},
				{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": {"path": "main.go"}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 12, "output_tokens": 7}
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := client.SendMessageWithTools(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []common.Message{common.NewUserMessage("read main.go")},
	}, []common.ToolDefinition{{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}})
	if err != nil {
		t.Fatalf("SendMessageWithTools: %v", err)
	}

	// Wire-level guard: the schema must arrive with properties/required at the
	// right nesting, not the whole schema stuffed under properties.
	var wire struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(requestBody, &wire); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(wire.Tools) != 1 {
		t.Fatalf("got %d tools on the wire, want 1: %s", len(wire.Tools), requestBody)
	}
	schema := wire.Tools[0].InputSchema
	if _, ok := schema.Properties["path"]; !ok {
		t.Errorf("wire schema properties missing path: %s", requestBody)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "path" {
		t.Errorf("wire schema required = %v, want [path]", schema.Required)
	}

	if res.Text() != "reading" {
		t.Errorf("text = %q, want reading", res.Text())
	}
	calls := res.ToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "read_file" {
		t.Fatalf("tool calls = %+v, want one read_file call", calls)
	}
	if res.StopReason != common.StopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", res.StopReason, common.StopReasonToolUse)
	}
	if res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want 12/7", res.Usage)
	}
}

func TestAnthropic_SendSyncMessage(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
			"content": [{"type": "text", "text": "hello"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 5, "output_tokens": 2}
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		Messages:  []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}
	if res.Text() != "hello" {
		t.Errorf("text = %q, want hello", res.Text())
	}
	if res.StopReason != common.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want %q", res.StopReason, common.StopReasonEndTurn)
	}
}

func TestAnthropic_SyncCapsMaxTokens(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		maxTokens     int64
		wantMaxTokens int64
	}{
		// Model default 128000 exceeds the SDK's 10-minute guard
		// (128000/6 = 21333), which rejects the request client-side.
		{"default capped to 10-minute bound", "claude-sonnet-4-6", 0, 21333},
		{"explicit over bound capped", "claude-sonnet-4-6", 30000, 21333},
		{"under bound untouched", "claude-sonnet-4-6", 4096, 4096},
		{"per-model SDK limit wins", "claude-opus-4-1-20250805", 30000, 8192},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody []byte
			client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var err error
				requestBody, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{
					"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
					"content": [{"type": "text", "text": "ok"}],
					"stop_reason": "end_turn",
					"usage": {"input_tokens": 5, "output_tokens": 2}
				}`)); err != nil {
					t.Errorf("write response: %v", err)
				}
			})

			_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:     tt.model,
				MaxTokens: tt.maxTokens,
				Messages:  []common.Message{common.NewUserMessage("hi")},
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			var wire struct {
				MaxTokens int64 `json:"max_tokens"`
			}
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if wire.MaxTokens != tt.wantMaxTokens {
				t.Errorf("wire max_tokens = %d, want %d", wire.MaxTokens, tt.wantMaxTokens)
			}
		})
	}
}

func TestAnthropic_StreamingDoesNotCapMaxTokens(t *testing.T) {
	var requestBody []byte
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
		writeSSE(t, w, "message_stop", `{"type":"message_stop"}`)
	})

	events := make(chan common.StreamEvent, 8)
	if err := client.SendStreamingMessage(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}, events); err != nil {
		t.Fatalf("SendStreamingMessage: %v", err)
	}
	for range events {
	}

	var wire struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(requestBody, &wire); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	wantMax := int64(testModel.GetMaxTokens())
	if wire.MaxTokens != wantMax {
		t.Errorf("wire max_tokens = %d, want uncapped model default %d", wire.MaxTokens, wantMax)
	}
}

func TestAnthropic_CountTokens(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/count_tokens") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"input_tokens": 42}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	count, err := client.CountTokens(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		System:   "be helpful",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if count.InputTokens != 42 {
		t.Errorf("input tokens = %d, want 42", count.InputTokens)
	}
}

func TestAnthropic_ListModels(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"data": [{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6", "type": "model", "created_at": "2026-01-01T00:00:00Z"}],
			"has_more": false,
			"first_id": null,
			"last_id": null
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "claude-sonnet-4-6" || models[0].Name != "Claude Sonnet 4.6" {
		t.Errorf("model = %+v, want claude-sonnet-4-6/Claude Sonnet 4.6", models[0])
	}
}

func TestAnthropic_GetCurrentModel(t *testing.T) {
	client, err := NewClient(testModel, WithAPIKey("test"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.GetCurrentModel(); got != testModel.GetName() {
		t.Errorf("GetCurrentModel() = %q, want %q", got, testModel.GetName())
	}
}

func TestAnthropic_NewClientRequiresModel(t *testing.T) {
	if _, err := NewClient(nil, WithAPIKey("test")); err == nil {
		t.Fatal("NewClient without Model: want error, got nil")
	}
}

// The default client is unlimited; WithRateLimit opts into the limiter.
func TestAnthropic_RateLimitOptIn(t *testing.T) {
	unlimited, err := NewClient(testModel, WithAPIKey("test"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, ok := unlimited.(*Client); !ok {
		t.Error("default client should be the raw unlimited client")
	}

	limited, err := NewClient(testModel, WithAPIKey("test"),
		WithRateLimit(10_000.0/60.0, 10_000), WithMaxConcurrency(10))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, ok := limited.(*Client); ok {
		t.Error("WithRateLimit should wrap the raw client in a limiter")
	}

	if _, err := NewClient(testModel, WithAPIKey("test"), WithRateLimit(100, 0)); err == nil {
		t.Error("WithRateLimit without burst size: want error, got nil")
	}
}
