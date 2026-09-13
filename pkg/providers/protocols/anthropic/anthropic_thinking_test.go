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

func int64Ptr(v int64) *int64 { return &v }

func TestAnthropic_ThinkingBudgetOnWire(t *testing.T) {
	tests := []struct {
		name       string
		budget     int64
		wantBudget int64
	}{
		{"budget passed through", 8192, 8192},
		{"sub-1024 clamps up", 512, 1024},
	}

	think := true
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
				Model:          "claude-sonnet-4-6",
				MaxTokens:      16384,
				Messages:       []common.Message{common.NewUserMessage("hi")},
				Think:          &think,
				ThinkingBudget: int64Ptr(tt.budget),
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			var wire struct {
				Thinking struct {
					Type         string `json:"type"`
					BudgetTokens int64  `json:"budget_tokens"`
				} `json:"thinking"`
			}
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if wire.Thinking.Type != "enabled" {
				t.Errorf("thinking type = %q, want enabled: %s", wire.Thinking.Type, requestBody)
			}
			if wire.Thinking.BudgetTokens != tt.wantBudget {
				t.Errorf("budget_tokens = %d, want %d", wire.Thinking.BudgetTokens, tt.wantBudget)
			}
		})
	}
}

func TestAnthropic_ThinkingBudgetValidation(t *testing.T) {
	think := true
	tests := []struct {
		name    string
		req     common.CompletionRequest
		wantErr string
	}{
		{
			"budget at MaxTokens",
			common.CompletionRequest{Think: &think, MaxTokens: 2048, ThinkingBudget: int64Ptr(2048)},
			"must be less than MaxTokens",
		},
		{
			"budget above MaxTokens",
			common.CompletionRequest{Think: &think, MaxTokens: 2048, ThinkingBudget: int64Ptr(4096)},
			"must be less than MaxTokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newAnthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("request should not reach the server")
			})
			req := tt.req
			req.Model = "claude-sonnet-4-6"
			req.Messages = []common.Message{common.NewUserMessage("hi")}
			_, err := client.SendSyncMessage(context.Background(), req)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestAnthropic_ThinkingBudgetImpliesThink pins the Think-nil semantics: a
// budget alone enables extended thinking on the wire (the API rejects
// budget_tokens without it), while an explicit Think=false leaves the
// budget inert instead of erroring.
func TestAnthropic_ThinkingBudgetImpliesThink(t *testing.T) {
	noThink := false
	tests := []struct {
		name         string
		think        *bool
		wantThinking bool
	}{
		{"Think nil enables thinking", nil, true},
		{"Think false leaves the budget inert", &noThink, false},
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
				Model:          "claude-sonnet-4-6",
				MaxTokens:      16384,
				Messages:       []common.Message{common.NewUserMessage("hi")},
				Think:          tt.think,
				ThinkingBudget: int64Ptr(4096),
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			var wire map[string]json.RawMessage
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			_, present := wire["thinking"]
			if present != tt.wantThinking {
				t.Errorf("thinking present = %v, want %v: %s", present, tt.wantThinking, requestBody)
			}
			if !tt.wantThinking {
				return
			}
			var thinking struct {
				Type         string `json:"type"`
				BudgetTokens int64  `json:"budget_tokens"`
			}
			if err := json.Unmarshal(wire["thinking"], &thinking); err != nil {
				t.Fatalf("unmarshal thinking: %v", err)
			}
			if thinking.Type != "enabled" || thinking.BudgetTokens != 4096 {
				t.Errorf("thinking = %+v, want enabled/4096", thinking)
			}
		})
	}
}
