package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

func int64Ptr(v int64) *int64 { return &v }

func TestOllama_ThinkingBudgetIgnored(t *testing.T) {
	bodies := make([][]byte, 0, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`))
	}))
	t.Cleanup(srv.Close)
	client := mustNewClient(t, WithBaseURL(srv.URL)).(*Client)

	req := common.CompletionRequest{
		Model:    "test-model",
		Messages: []common.Message{common.NewUserMessage("hi")},
	}
	for _, budget := range []*int64{nil, int64Ptr(8192)} {
		req.ThinkingBudget = budget
		if _, err := client.SendSyncMessage(context.Background(), req); err != nil {
			t.Fatalf("SendSyncMessage(budget=%v): %v", budget, err)
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Errorf("request body changed with ThinkingBudget set:\nunset: %s\nset:   %s", bodies[0], bodies[1])
	}
}

func TestOllama_ReasoningEffortSetsThinkLevel(t *testing.T) {
	tests := []struct {
		name   string
		effort string
		think  *bool
		want   any // nil means the field must be absent
	}{
		{"no effort, no preference omits think", "", nil, nil},
		{"no effort keeps the boolean", "", boolPtr(true), true},
		{"effort replaces an unset boolean", "max", nil, "max"},
		{"effort replaces think:true", "high", boolPtr(true), "high"},
		{"explicit think:false beats the effort", "high", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`))
			}))
			t.Cleanup(srv.Close)

			opts := []ClientOption{WithBaseURL(srv.URL)}
			if tt.effort != "" {
				opts = append(opts, WithReasoningEffort(tt.effort))
			}
			client := mustNewClient(t, opts...)

			_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
				Model:    "test-model",
				Messages: []common.Message{common.NewUserMessage("hi")},
				Think:    tt.think,
			})
			if err != nil {
				t.Fatalf("SendSyncMessage: %v", err)
			}

			var wire map[string]any
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			got, present := wire["think"]
			if tt.want == nil {
				if present {
					t.Errorf("think = %v on the wire, want absent", got)
				}
				return
			}
			if !present {
				t.Fatalf("think absent from request body: %s", body)
			}
			if got != tt.want {
				t.Errorf("think = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}
