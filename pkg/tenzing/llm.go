package tenzing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/openai_compat"
)

// Prompt is one single-turn request: a user message, plus optional system
// prompt, reasoning switch and streaming callbacks.
type Prompt struct {
	// Text is the user message. Required.
	Text string
	// SystemPrompt is optional; empty sends no system message.
	SystemPrompt string
	// Thinking turns model reasoning on or off; nil keeps the provider's
	// default.
	Thinking *bool
	// OnText, when set, streams the answer: it is called with each piece as
	// it arrives. Nil makes one non-streaming request.
	OnText func(string)
	// OnThinking receives reasoning pieces while streaming. Optional; ignored
	// unless OnText is set.
	OnThinking func(string)
}

// LLMCall is one request to an OpenAI-compatible model. Every connection
// field is required: there is no default endpoint, model or token limit.
type LLMCall struct {
	APIKey string
	// URL is the full chat-completions endpoint, used exactly as given —
	// nothing is appended, e.g. https://openrouter.ai/api/v1/chat/completions.
	URL       string
	Model     string
	MaxTokens int
	// HTTPTimeout bounds each HTTP attempt end to end; zero means none.
	// A timed-out attempt is retried, so size it above the slowest answer.
	HTTPTimeout time.Duration
	Prompt      Prompt
}

// LLMResponse is the model's reply; Text() returns the answer.
type LLMResponse = common.CompletionResponse

// CallLLM sends one prompt to an OpenAI-compatible model and returns its
// reply, streaming it through Prompt.OnText when that is set. No agent loop,
// tools or session. It builds a client per call; for repeated calls build a
// common.LLM once and use PromptLLM.
func CallLLM(ctx context.Context, c LLMCall) (LLMResponse, error) {
	if err := c.validate(); err != nil {
		return LLMResponse{}, fmt.Errorf("CallLLM: %w", err)
	}
	llm, err := openai_compat.NewClient(
		common.ModelDefinition{Name: c.Model, MaxTokens: c.MaxTokens, Provider: "openai_compat"},
		openai_compat.WithName("openai_compat"),
		openai_compat.WithAPIKey(c.APIKey),
		openai_compat.WithEndpointURL(c.URL),
		openai_compat.WithHTTPTimeout(c.HTTPTimeout))
	if err != nil {
		return LLMResponse{}, fmt.Errorf("CallLLM: %w", err)
	}
	resp, err := PromptLLM(ctx, llm, c.Prompt)
	if err != nil {
		return LLMResponse{}, fmt.Errorf("CallLLM: %w", err)
	}
	return resp, nil
}

func (c LLMCall) validate() error {
	switch {
	case c.APIKey == "":
		return errors.New("APIKey is required")
	case c.URL == "":
		return errors.New("URL is required")
	case c.Model == "":
		return errors.New("Model is required")
	case c.MaxTokens <= 0:
		return errors.New("MaxTokens must be positive")
	}
	return nil
}

// PromptLLM sends one prompt to an existing client — any common.LLM, of any
// protocol — and returns the reply. It is CallLLM without the client
// construction, and what `tenzing single` runs.
func PromptLLM(ctx context.Context, llm common.LLM, p Prompt) (LLMResponse, error) {
	if strings.TrimSpace(p.Text) == "" {
		return LLMResponse{}, errors.New("prompt text is required")
	}
	req := common.CompletionRequest{
		Model:    llm.GetCurrentModel(),
		Messages: []common.Message{common.NewUserMessage(p.Text)},
		System:   p.SystemPrompt,
		Think:    p.Thinking,
	}
	if p.OnText == nil {
		return llm.SendSyncMessage(ctx, req)
	}
	return streamPrompt(ctx, llm, req, p)
}

// streamPrompt relays deltas to the callbacks and returns the final
// response. The channel is read to the end even after an error event: the
// client closes it on return, and leaving it unread would block the client.
func streamPrompt(ctx context.Context, llm common.LLM, req common.CompletionRequest, p Prompt) (LLMResponse, error) {
	events := make(chan common.StreamEvent)
	done := make(chan error, 1)
	go func() { done <- llm.SendStreamingMessage(ctx, req, events) }()

	var resp LLMResponse
	var streamErr error
	for ev := range events {
		switch ev.Type {
		case common.StreamEventDelta:
			p.OnText(ev.Text)
		case common.StreamEventThinking:
			if p.OnThinking != nil {
				p.OnThinking(ev.Text)
			}
		case common.StreamEventStop:
			if ev.Response != nil {
				resp = *ev.Response
			}
		case common.StreamEventError:
			streamErr = ev.Err
		}
	}
	if err := <-done; err != nil {
		return LLMResponse{}, err
	}
	if streamErr != nil {
		return LLMResponse{}, streamErr
	}
	return resp, nil
}
