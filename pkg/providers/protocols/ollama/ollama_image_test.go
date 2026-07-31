package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Image blocks in user messages ride Ollama's native images field as bare
// base64 strings; the message text is unaffected.
func TestOllama_ImageBlocksWireShape(t *testing.T) {
	var requestBody []byte
	client := newOllamaTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"model": "test-model",
			"message": {"role": "assistant", "content": "a cat"},
			"done": true, "done_reason": "stop",
			"prompt_eval_count": 8, "eval_count": 4
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model: "test-model",
		Messages: []common.Message{{
			Role: common.RoleUser,
			Content: []common.ContentBlock{
				common.NewTextContent("what is this?"),
				common.NewImageContent("image/png", "aGVsbG8="),
				{Type: common.ContentTypeImage}, // nil Image: dropped, not crashed
			},
		}},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	var req struct {
		Messages []struct {
			Role    string   `json:"role"`
			Content string   `json:"content"`
			Images  []string `json:"images"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(req.Messages))
	}
	msg := req.Messages[0]
	if msg.Content != "what is this?" {
		t.Errorf("content = %q, want text only", msg.Content)
	}
	if len(msg.Images) != 1 || msg.Images[0] != "aGVsbG8=" {
		t.Errorf("images = %#v, want [aGVsbG8=]", msg.Images)
	}
}
