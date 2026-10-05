package tenzing_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/tenzing"
)

func TestCallLLM(t *testing.T) {
	var path string
	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"vendor/x","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"42"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	resp, err := tenzing.CallLLM(context.Background(), tenzing.LLMCall{
		APIKey:    "k",
		URL:       srv.URL + "/custom/endpoint", // used as given: no /chat/completions
		Model:     "vendor/x",
		MaxTokens: 256,
		Prompt:    tenzing.Prompt{Text: "what is 6*7?", SystemPrompt: "Be terse."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/custom/endpoint" {
		t.Errorf("path = %q, want /custom/endpoint", path)
	}
	if got.Model != "vendor/x" || got.MaxTokens != 256 || len(got.Messages) != 2 ||
		got.Messages[0].Role != "system" || got.Messages[1].Content != "what is 6*7?" {
		t.Errorf("request = %+v", got)
	}
	if resp.Text() != "42" {
		t.Errorf("Text() = %q, want 42", resp.Text())
	}
}

// Nothing falls back to a default endpoint, model or token limit.
func TestCallLLMRequiresFields(t *testing.T) {
	full := tenzing.LLMCall{APIKey: "k", URL: "http://127.0.0.1:1/x", Model: "m", MaxTokens: 1, Prompt: tenzing.Prompt{Text: "hi"}}
	for _, tt := range []struct {
		name, want string
		edit       func(*tenzing.LLMCall)
	}{
		{"no key", "APIKey is required", func(c *tenzing.LLMCall) { c.APIKey = "" }},
		{"no URL", "URL is required", func(c *tenzing.LLMCall) { c.URL = "" }},
		{"relative URL", "must be an absolute URL", func(c *tenzing.LLMCall) { c.URL = "/chat" }},
		{"no model", "Model is required", func(c *tenzing.LLMCall) { c.Model = "" }},
		{"no max tokens", "MaxTokens must be positive", func(c *tenzing.LLMCall) { c.MaxTokens = 0 }},
		{"no prompt", "prompt text is required", func(c *tenzing.LLMCall) { c.Prompt.Text = " " }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := full
			tt.edit(&c)
			_, err := tenzing.CallLLM(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// With OnText set the answer streams piece by piece, to the same exact URL,
// and the final response still comes back.
func TestCallLLMStreams(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{"4", "2"} {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"vendor/x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", piece)
		}
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"vendor/x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	var pieces []string
	resp, err := tenzing.CallLLM(context.Background(), tenzing.LLMCall{
		APIKey: "k", URL: srv.URL + "/custom/endpoint", Model: "vendor/x", MaxTokens: 16,
		Prompt: tenzing.Prompt{Text: "6*7?", OnText: func(s string) { pieces = append(pieces, s) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/custom/endpoint" {
		t.Errorf("path = %q, want /custom/endpoint", path)
	}
	if strings.Join(pieces, "|") != "4|2" || resp.Text() != "42" {
		t.Errorf("pieces = %q, Text() = %q", pieces, resp.Text())
	}
}
