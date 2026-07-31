package openai_compat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

const imageTestResponse = `{
	"id": "chatcmpl-1", "object": "chat.completion", "model": "test-model",
	"choices": [{"index": 0, "message": {"role": "assistant", "content": "a cat"}, "finish_reason": "stop"}],
	"usage": {"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13}
}`

// User messages with images marshal as content-part arrays with data: URI
// image_url parts; text-only user messages stay plain strings.
func TestOpenAICompat_ImageBlocksWireShape(t *testing.T) {
	var requestBody []byte
	compat := newCompatTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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

	_, err := compat.SendSyncMessage(context.Background(), common.CompletionRequest{
		Model: "test-model",
		Messages: []common.Message{
			common.NewUserMessage("plain text turn"),
			{
				Role: common.RoleUser,
				Content: []common.ContentBlock{
					common.NewTextContent("what is this?"),
					common.NewImageContent("image/png", "aGVsbG8="),
					{Type: common.ContentTypeImage}, // nil Image: dropped, not crashed
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendSyncMessage: %v", err)
	}

	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(req.Messages))
	}

	// text-only message keeps the plain-string content shape
	var plain string
	if err := json.Unmarshal(req.Messages[0].Content, &plain); err != nil {
		t.Errorf("text-only content is not a plain string: %s", req.Messages[0].Content)
	} else if plain != "plain text turn" {
		t.Errorf("text-only content = %q", plain)
	}

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(req.Messages[1].Content, &parts); err != nil {
		t.Fatalf("image message content is not a part array: %s", req.Messages[1].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2 (text + image; nil-source image dropped): %s", len(parts), req.Messages[1].Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "what is this?" {
		t.Errorf("part 0 = %+v, want text part", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("part 1 = %+v, want data: URI image_url", parts[1])
	}
}
