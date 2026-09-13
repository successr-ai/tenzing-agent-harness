package openai_compat

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

func TestOpenAICompat_ThinkingBudgetMapsToReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		budget     *int64
		wantEffort string // "" means field must be absent
	}{
		{"nil budget sends no field", nil, ""},
		{"tiny budget is low", int64Ptr(1024), "low"},
		{"below 4096 is low", int64Ptr(4095), "low"},
		{"4096 is medium", int64Ptr(4096), "medium"},
		{"below 16384 is medium", int64Ptr(16383), "medium"},
		{"16384 is high", int64Ptr(16384), "high"},
		{"huge budget is high", int64Ptr(100000), "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody []byte
			compat := newCompatTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(compatCompletionJSON))
			})

			_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:          "test-model",
				Messages:       []common.Message{common.NewUserMessage("hi")},
				ThinkingBudget: tt.budget,
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			if tt.wantEffort == "" {
				if strings.Contains(string(requestBody), "reasoning_effort") {
					t.Errorf("nil budget but reasoning_effort on the wire: %s", requestBody)
				}
				return
			}
			var wire struct {
				ReasoningEffort string `json:"reasoning_effort"`
			}
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if wire.ReasoningEffort != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", wire.ReasoningEffort, tt.wantEffort)
			}
		})
	}
}

// TestOpenAICompat_BudgetOverridesReasoningEffort pins the precedence: a
// request ThinkingBudget's tier beats the client's configured effort; the
// configured effort applies only to requests without a budget.
func TestOpenAICompat_BudgetOverridesReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		effort     string
		budget     *int64
		wantEffort string
	}{
		{"configured effort with no budget", "low", nil, "low"},
		{"budget tier beats the configured effort", "max", int64Ptr(100000), "high"},
		{"empty effort falls back to the budget tier", "", int64Ptr(1024), "low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody []byte
			compat := newCompatTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(compatCompletionJSON))
			})
			compat.ReasoningEffort = tt.effort

			_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:          "test-model",
				Messages:       []common.Message{common.NewUserMessage("hi")},
				ThinkingBudget: tt.budget,
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			var wire struct {
				ReasoningEffort string `json:"reasoning_effort"`
			}
			if err := json.Unmarshal(requestBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if wire.ReasoningEffort != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", wire.ReasoningEffort, tt.wantEffort)
			}
		})
	}
}
