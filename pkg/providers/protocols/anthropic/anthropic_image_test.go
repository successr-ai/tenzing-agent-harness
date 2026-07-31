package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const imageTestResponse = `{
	"id": "msg_1", "type": "message", "role": "assistant",
	"model": "claude-sonnet-4-6",
	"content": [{"type": "text", "text": "a cat"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 10, "output_tokens": 3}
}`

// Image content blocks marshal to Anthropic's base64 image source shape.
func TestAnthropic_ImageBlocksWireShape(t *testing.T) {
	var requestBody []byte
	client := newAnthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(imageTestResponse)); err != nil {
			t.Errorf("write response: %v", err)
		}
	})

	_, err := client.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model: "claude-sonnet-4-6",
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
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(req.Messages))
	}
	content := req.Messages[0].Content
	if len(content) != 2 {
		t.Fatalf("got %d content blocks, want 2 (text + image; nil-source image dropped): %s", len(content), requestBody)
	}
	if content[0].Type != "text" || content[0].Text != "what is this?" {
		t.Errorf("block 0 = %+v, want text block", content[0])
	}
	img := content[1]
	if img.Type != "image" || img.Source.Type != "base64" ||
		img.Source.MediaType != "image/png" || img.Source.Data != "aGVsbG8=" {
		t.Errorf("block 1 = %+v, want base64 image/png source", img)
	}
}
