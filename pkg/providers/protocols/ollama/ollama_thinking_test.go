package ollama

import (
	"bytes"
	"context"
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
