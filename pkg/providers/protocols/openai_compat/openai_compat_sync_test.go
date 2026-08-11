package openai_compat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newCompatTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	return &Client{Name: "test", Client: &client, Model: common.ModelDefinition{Name: "test-model"}}
}

func TestOpenAICompat_SendSyncMessage(t *testing.T) {
	compat := newCompatTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "test-model",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "reading",
					"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"main.go\"}"}}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	res, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("read main.go")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	if res.Text() != "reading" {
		t.Errorf("text = %q, want reading", res.Text())
	}
	calls := res.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].ToolUseID != "call_1" || calls[0].ToolName != "read_file" {
		t.Errorf("tool call = %s/%s, want call_1/read_file", calls[0].ToolUseID, calls[0].ToolName)
	}
	if res.StopReason != common.StopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", res.StopReason, common.StopReasonToolUse)
	}
	if res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want 12/7", res.Usage)
	}
}

func TestOpenAICompat_SendMessageWithToolsWireShape(t *testing.T) {
	var requestBody []byte
	compat := newCompatTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(compatCompletionJSON)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	_, err := compat.SendMessageWithTools(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}, []common.ToolDefinition{{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}})
	if err != nil {
		t.Fatalf("SendMessageWithTools: %v", err)
	}

	var wire struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string `json:"name"`
				Parameters struct {
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(requestBody, &wire); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(wire.Tools) != 1 {
		t.Fatalf("got %d tools on the wire, want 1: %s", len(wire.Tools), requestBody)
	}
	fn := wire.Tools[0].Function
	if fn.Name != "read_file" {
		t.Errorf("wire tool name = %q, want read_file", fn.Name)
	}
	if _, ok := fn.Parameters.Properties["path"]; !ok {
		t.Errorf("wire parameters missing path property: %s", requestBody)
	}
	if len(fn.Parameters.Required) != 1 || fn.Parameters.Required[0] != "path" {
		t.Errorf("wire parameters required = %v, want [path]", fn.Parameters.Required)
	}
}

func TestOpenAICompat_BadToolSchemaRejected(t *testing.T) {
	compat := newCompatTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request should not reach the server")
	})

	_, err := compat.SendMessageWithTools(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}, []common.ToolDefinition{{
		Name:        "bad",
		InputSchema: json.RawMessage(`{not json`),
	}})
	if err == nil {
		t.Fatal("want error for invalid schema, got nil")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the tool: %v", err)
	}
}

func TestOpenAICompat_ListModels(t *testing.T) {
	compat := newCompatTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"object": "list", "data": [
			{"id": "model-a", "object": "model", "created": 0, "owned_by": "test"},
			{"id": "model-b", "object": "model", "created": 0, "owned_by": "test"}
		]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	models, err := compat.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Errorf("models = %+v, want model-a and model-b", models)
	}
}

// NewClient wraps limiting internally: WithRateLimit + WithMaxConcurrency
// produce a guarded client, no caller-side ratelimit.Wrap needed.
func TestOpenAICompat_RateLimitAcquireRelease(t *testing.T) {
	srv := httptest.NewServer(respondCompletion(t))
	t.Cleanup(srv.Close)

	limited, err := NewClient(common.ModelDefinition{Name: "test-model"},
		WithName("test"), WithAPIKey("key"), WithBaseURL(srv.URL),
		WithRateLimit(100_000, 100_000),
		WithMaxConcurrency(1),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, ok := limited.(*Client); ok {
		t.Fatal("NewClient with limits returned the raw *Client, want wrapped")
	}

	// Two sequential sends must both succeed: if Release is broken, the
	// second send deadlocks on the MaxConcurrency=1 semaphore.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := range 2 {
		if _, err := limited.SendSyncMessage(ctx, common.CompletionRequest{
			Model:    "test-model",
			Messages: []common.Message{common.NewUserMessage("hi")},
		}); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
}

func TestOpenAICompat_NewClientRequiresModel(t *testing.T) {
	if _, err := NewClient(nil, WithName("test")); err == nil {
		t.Fatal("NewClient without Model: want error, got nil")
	}

	llm, err := NewClient(common.ModelDefinition{Name: "m"},
		WithName("test"), WithAPIKey("key"), WithBaseURL("https://example.test"))
	if err != nil {
		t.Fatalf("NewClient with Model: %v", err)
	}
	client, ok := llm.(*Client)
	if !ok {
		t.Fatalf("NewClient without limits = %T, want unwrapped *Client", llm)
	}
	if client.Name != "test" || client.GetCurrentModel() != "m" {
		t.Errorf("client = %s/%s, want test/m", client.Name, client.GetCurrentModel())
	}
	if client.RetryBackoff == nil || *client.RetryBackoff != ratelimit.NewDefaultBackoff() {
		t.Errorf("RetryBackoff = %+v, want defaults (429 retry on by default)", client.RetryBackoff)
	}
}

func TestOpenAICompat_WithExtraFieldWireShape(t *testing.T) {
	var requestBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		respondCompletion(t)(w, r)
	}))
	t.Cleanup(srv.Close)

	llm, err := NewClient(common.ModelDefinition{Name: "test-model"},
		WithName("test"), WithAPIKey("key"), WithBaseURL(srv.URL),
		WithExtraField("provider.sort", "throughput"),
		WithExtraField("provider.allow_fallbacks", false),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := llm.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	var wire struct {
		Provider struct {
			Sort           string `json:"sort"`
			AllowFallbacks *bool  `json:"allow_fallbacks"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(requestBody, &wire); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if wire.Provider.Sort != "throughput" {
		t.Errorf("wire provider.sort = %q, want throughput: %s", wire.Provider.Sort, requestBody)
	}
	if wire.Provider.AllowFallbacks == nil || *wire.Provider.AllowFallbacks {
		t.Errorf("wire provider.allow_fallbacks = %v, want false: %s", wire.Provider.AllowFallbacks, requestBody)
	}
}

func TestOpenAICompat_CountTokensEstimate(t *testing.T) {
	compat := &Client{Name: "test"}

	count, err := compat.CountTokens(context.Background(), common.CompletionRequest{
		System: strings.Repeat("s", 40),
		Messages: []common.Message{
			{Role: common.RoleUser, Content: []common.ContentBlock{common.NewTextContent(strings.Repeat("m", 60))}},
		},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	// (40 + 60) chars / 4 chars per token.
	if count.InputTokens != 25 {
		t.Errorf("input tokens = %d, want 25", count.InputTokens)
	}
}

func TestOpenAICompat_CacheSystemAndToolsIgnored(t *testing.T) {
	bodies := make([][]byte, 0, 2)
	compat := newCompatTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "test-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	})

	req := common.CompletionRequest{
		Model:    "test-model",
		System:   "be helpful",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}
	for _, flag := range []bool{false, true} {
		req.CacheSystemAndTools = flag
		if _, err := compat.SendSyncMessage(context.Background(), req); err != nil {
			t.Fatalf("SendSyncMessage(flag=%v): %v", flag, err)
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Errorf("request body changed with CacheSystemAndTools set:\nunset: %s\nset:   %s", bodies[0], bodies[1])
	}
}
