package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// cacheWire is the request-body shape relevant to cache_control placement.
type cacheWire struct {
	System []struct {
		Text         string          `json:"text"`
		CacheControl json.RawMessage `json:"cache_control"`
	} `json:"system"`
	Tools []struct {
		Name         string          `json:"name"`
		CacheControl json.RawMessage `json:"cache_control"`
	} `json:"tools"`
}

func twoTools() []common.ToolDefinition {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	return []common.ToolDefinition{
		{Name: "first_tool", Description: "a", InputSchema: schema},
		{Name: "second_tool", Description: "b", InputSchema: schema},
	}
}

func TestAnthropic_CacheSystemAndToolsRequestShape(t *testing.T) {
	tests := []struct {
		name string
		flag bool
	}{
		{"flag set places breakpoints", true},
		{"flag unset sends no cache_control", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody []byte
			client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
					"content": [{"type": "text", "text": "ok"}],
					"stop_reason": "end_turn",
					"usage": {"input_tokens": 5, "output_tokens": 2}
				}`))
			})

			_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:               "claude-sonnet-4-6",
				System:              "be helpful",
				Tools:               twoTools(),
				Messages:            []common.Message{common.NewUserMessage("hi")},
				CacheSystemAndTools: tt.flag,
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			if !tt.flag {
				if strings.Contains(string(requestBody), "cache_control") {
					t.Fatalf("flag unset but cache_control on the wire: %s", requestBody)
				}
				return
			}

			var wire cacheWire
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if len(wire.System) == 0 {
				t.Fatalf("no system blocks on the wire: %s", requestBody)
			}
			last := wire.System[len(wire.System)-1]
			if !strings.Contains(string(last.CacheControl), "ephemeral") {
				t.Errorf("last system block cache_control = %s, want ephemeral", last.CacheControl)
			}
			if len(wire.Tools) != 2 {
				t.Fatalf("got %d tools on the wire, want 2: %s", len(wire.Tools), requestBody)
			}
			if wire.Tools[0].CacheControl != nil {
				t.Errorf("first tool has cache_control, want breakpoint only on last: %s", wire.Tools[0].CacheControl)
			}
			if !strings.Contains(string(wire.Tools[1].CacheControl), "ephemeral") {
				t.Errorf("last tool cache_control = %s, want ephemeral", wire.Tools[1].CacheControl)
			}
		})
	}
}

func TestAnthropic_CacheTokensInUsageSync(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
			"content": [{"type": "text", "text": "ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 5, "output_tokens": 2, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 200}
		}`))
	})

	res, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []common.Message{common.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}
	if res.Usage.CacheReadInputTokens != 100 || res.Usage.CacheCreationInputTokens != 200 {
		t.Errorf("cache usage = %d/%d, want 100/200",
			res.Usage.CacheReadInputTokens, res.Usage.CacheCreationInputTokens)
	}
}

func TestAnthropic_CacheTokensInUsageStreaming(t *testing.T) {
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":200}}}`)
		writeSSE(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeSSE(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		writeSSE(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeSSE(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`)
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

	_, response := collectEvents(t, events)
	if err := <-errCh; err != nil {
		t.Fatalf("SendStreamingMessage: %v", err)
	}
	if response.Usage.CacheReadInputTokens != 100 || response.Usage.CacheCreationInputTokens != 200 {
		t.Errorf("cache usage = %d/%d, want 100/200",
			response.Usage.CacheReadInputTokens, response.Usage.CacheCreationInputTokens)
	}
}

// collectEvents drains a stream of events, returning the text deltas and the
// final response. Fails the test on error events or a missing stop event.
func collectEvents(t *testing.T, events <-chan common.StreamEvent) ([]string, *common.CompletionResponse) {
	t.Helper()
	var deltas []string
	var response *common.CompletionResponse
	for ev := range events {
		switch ev.Type {
		case common.StreamEventDelta:
			deltas = append(deltas, ev.Text)
		case common.StreamEventStop:
			response = ev.Response
		case common.StreamEventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if response == nil {
		t.Fatal("stream ended without a stop event")
	}
	return deltas, response
}
