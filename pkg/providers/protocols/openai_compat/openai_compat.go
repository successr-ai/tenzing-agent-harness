package openai_compat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

type Model = common.Model

type ClientOption func(*clientOptions)

type clientOptions struct {
	name                   string
	model                  Model
	apiKey                 string
	baseURL                string
	retryBackoff           *ratelimit.RetryBackoff
	useMaxCompletionTokens bool
	extraFields            []extraField
	// rateLimit configures the client-side limiter: Rate+BurstSize enable the
	// token bucket, MaxConcurrency bounds in-flight requests. All zero means
	// unlimited.
	rateLimit *ratelimit.TokenBucketConfig
}

func loadClientOptions(model Model, opts []ClientOption) *clientOptions {
	backoff := ratelimit.NewDefaultBackoff()
	o := &clientOptions{
		model:        model,
		retryBackoff: &backoff,
		rateLimit: &ratelimit.TokenBucketConfig{
			Rate:           0,
			BurstSize:      0,
			MaxConcurrency: 0,
		},
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithName sets the provider name used in error messages and logs.
func WithName(name string) ClientOption {
	return func(o *clientOptions) {
		o.name = name
	}
}

// WithAPIKey sets the API key sent to the provider.
func WithAPIKey(key string) ClientOption {
	return func(o *clientOptions) {
		o.apiKey = key
	}
}

// WithBaseURL points the client at the provider's OpenAI-compatible endpoint.
func WithBaseURL(baseURL string) ClientOption {
	return func(o *clientOptions) {
		o.baseURL = baseURL
	}
}

// WithRetryBackoff overrides the 429 retry backoff, which is enabled by
// default with the ratelimit.NewDefaultBackoff values. Zero cfg fields keep
// their defaults.
func WithRetryBackoff(cfg ratelimit.RetryBackoff) ClientOption {
	return func(o *clientOptions) {
		cfg = cfg.OrDefaults()
		o.retryBackoff = &cfg
	}
}

// WithRateLimit enables a client-side token-bucket rate limiter costed by
// estimated input token count. Without it the client is unlimited.
func WithRateLimit(rate float64, burstSize int64) ClientOption {
	return func(o *clientOptions) {
		o.rateLimit.Rate = rate
		o.rateLimit.BurstSize = burstSize
	}
}

// WithMaxConcurrency bounds concurrent requests. Values <= 0 remove the
// bound (the default). Combined with WithRateLimit the bound lives inside
// the token bucket; alone it is a pure semaphore.
func WithMaxConcurrency(limit int) ClientOption {
	return func(o *clientOptions) {
		o.rateLimit.MaxConcurrency = int64(max(limit, 0))
	}
}

// extraField is a JSON body field injected into every request.
type extraField struct {
	path  string
	value any
}

// WithExtraField injects a JSON field into every request body, for
// provider-specific extensions the OpenAI API does not model (e.g.
// OpenRouter's "provider" routing object). Path is a dot-separated JSON
// path such as "provider.sort".
func WithExtraField(path string, value any) ClientOption {
	return func(o *clientOptions) {
		o.extraFields = append(o.extraFields, extraField{path: path, value: value})
	}
}

// WithMaxCompletionTokens sends max_completion_tokens instead of the
// deprecated max_tokens, required by newer OpenAI models.
func WithMaxCompletionTokens() ClientOption {
	return func(o *clientOptions) {
		o.useMaxCompletionTokens = true
	}
}

type Client struct {
	// Name identifies the provider in error messages and logs.
	Name   string
	Client *openai.Client
	// Model is required; it supplies the default max tokens and the
	// context window size.
	Model Model
	// RetryBackoff, when non-nil, retries requests that fail with HTTP 429
	// using exponential backoff per its fields (zero fields fall back to the
	// defaults). NewClient always sets it; nil disables 429 retries.
	RetryBackoff *ratelimit.RetryBackoff
	// UseMaxCompletionTokens sends max_completion_tokens instead of the
	// deprecated max_tokens, required by newer OpenAI models.
	UseMaxCompletionTokens bool
}

// NewClient builds an OpenAI-compatible client. Model is required; there is
// no default.
func NewClient(model Model, options ...ClientOption) (common.LLM, error) {
	opts := loadClientOptions(model, options)
	if opts.model == nil {
		return nil, fmt.Errorf("%s: Model is required", opts.name)
	}

	var reqOpts []option.RequestOption
	if opts.apiKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(opts.apiKey))
	}
	if opts.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(opts.baseURL))
	}
	for _, f := range opts.extraFields {
		reqOpts = append(reqOpts, option.WithJSONSet(f.path, f.value))
	}
	sdk := openai.NewClient(reqOpts...)

	raw := &Client{
		Name:                   opts.name,
		Client:                 &sdk,
		Model:                  opts.model,
		RetryBackoff:           opts.retryBackoff,
		UseMaxCompletionTokens: opts.useMaxCompletionTokens,
	}
	var llm common.LLM = raw
	isRateDefined := opts.rateLimit.Rate > 0
	isBurstDefined := opts.rateLimit.BurstSize > 0
	switch {
	case isRateDefined && isBurstDefined:
		// The bucket's MaxConcurrency field doubles as the in-flight bound,
		// so no separate semaphore wrap is needed.
		llm = ratelimit.Wrap(llm, ratelimit.NewTokenBucket(*opts.rateLimit), ratelimit.CostByTokenCount)
	case isRateDefined || isBurstDefined:
		return nil, fmt.Errorf("%s: rate limit requires positive rate and burst size", opts.name)
	case opts.rateLimit.MaxConcurrency > 0:
		llm = ratelimit.Wrap(llm, ratelimit.NewSemaphore(int(opts.rateLimit.MaxConcurrency)), ratelimit.CostPerRequest)
	}
	return llm, nil
}

// apiError wraps an SDK or transport error into *common.APIError so callers
// can classify it with errors.As. The thin provider wrappers set Name to
// their provider string, so it doubles as the APIError Provider.
func (c *Client) apiError(err error) *common.APIError {
	status := 0
	var sdkErr *openai.Error
	if errors.As(err, &sdkErr) {
		status = sdkErr.StatusCode
	}
	return &common.APIError{
		StatusCode: status,
		Provider:   c.Name,
		Message:    err.Error(),
		Err:        err,
	}
}

func (c *Client) SendSyncMessage(ctx context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	return c.send(ctx, req)
}

// SendMessageWithTools sends a completion request with the given tools,
// overriding any tools already set on the request.
func (c *Client) SendMessageWithTools(ctx context.Context, req common.CompletionRequest, tools []common.ToolDefinition) (common.CompletionResponse, error) {
	req.Tools = tools
	return c.send(ctx, req)
}

func (c *Client) send(ctx context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	params, err := c.buildParams(req)
	if err != nil {
		return common.CompletionResponse{}, err
	}

	return ratelimit.RetryOnRateLimit(ctx, c.Name, c.RetryBackoff, func() (common.CompletionResponse, error) {
		res, err := c.Client.Chat.Completions.New(ctx, params)
		if err != nil {
			return common.CompletionResponse{}, fmt.Errorf("%s send message: %w", c.Name, c.apiError(err))
		}
		return fromOpenAIResponse(res), nil
	})
}

// SendStreamingMessage streams a completion. The events channel is always
// closed before returning, including on error. Rate-limited attempts are
// retried only if no events have been emitted yet, so consumers never see
// duplicated deltas.
func (c *Client) SendStreamingMessage(ctx context.Context, req common.CompletionRequest, events chan<- common.StreamEvent) error {
	defer close(events)

	params, err := c.buildParams(req)
	if err != nil {
		return err
	}
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: param.NewOpt(true),
	}

	err = ratelimit.RetryStreaming(ctx, c.Name, c.RetryBackoff, func() (bool, error) {
		emitted, err := c.streamOnce(ctx, params, events)
		if err != nil {
			return emitted, c.apiError(err)
		}
		return emitted, nil
	})
	if err != nil {
		events <- common.StreamEvent{Type: common.StreamEventError, Err: err}
		return fmt.Errorf("%s streaming: %w", c.Name, err)
	}
	return nil
}

// streamOnce runs a single streaming attempt. It reports whether any events
// were emitted so the caller knows if a retry is safe.
func (c *Client) streamOnce(ctx context.Context, params openai.ChatCompletionNewParams, events chan<- common.StreamEvent) (bool, error) {
	stream := c.Client.Chat.Completions.NewStreaming(ctx, params)

	type pendingToolCall struct {
		id   string
		name string
		args strings.Builder
	}

	var accumulated common.CompletionResponse
	var text strings.Builder
	toolCalls := map[int64]*pendingToolCall{}
	emitted := false

	for stream.Next() {
		chunk := stream.Current()

		if accumulated.ID == "" && chunk.ID != "" {
			accumulated.ID = chunk.ID
			accumulated.Model = chunk.Model
		}
		if !emitted {
			events <- common.StreamEvent{Type: common.StreamEventStart}
			emitted = true
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				events <- common.StreamEvent{
					Type: common.StreamEventDelta,
					Text: choice.Delta.Content,
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				call := toolCalls[tc.Index]
				if call == nil {
					call = &pendingToolCall{}
					toolCalls[tc.Index] = call
				}
				if tc.ID != "" {
					call.id = tc.ID
				}
				if tc.Function.Name != "" {
					call.name = tc.Function.Name
				}
				call.args.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason != "" {
				accumulated.StopReason = fromOpenAIFinishReason(choice.FinishReason)
			}
		}

		if chunk.Usage.TotalTokens > 0 {
			accumulated.Usage = common.Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}
	}

	if err := stream.Err(); err != nil {
		return emitted, err
	}

	if text.Len() > 0 {
		accumulated.Content = append(accumulated.Content, common.NewTextContent(text.String()))
	}

	indexes := make([]int64, 0, len(toolCalls))
	for idx := range toolCalls {
		indexes = append(indexes, idx)
	}
	slices.Sort(indexes)
	for _, idx := range indexes {
		call := toolCalls[idx]
		args := normalizeToolArguments(call.args.String())
		accumulated.Content = append(accumulated.Content, common.NewToolUseContent(call.id, call.name, json.RawMessage(args)))
	}

	events <- common.StreamEvent{
		Type:     common.StreamEventStop,
		Response: &accumulated,
	}
	return emitted, nil
}

func (c *Client) GetCurrentModel() string {
	return c.Model.GetName()
}

func (c *Client) GetContextWindowSize() int {
	return c.Model.GetContextWindowSize()
}

func (c *Client) GetModel() common.Model {
	return c.Model
}

// CountTokens estimates input tokens using the ~4 chars per token rule of
// thumb; OpenAI-compatible APIs have no token counting endpoint.
func (c *Client) CountTokens(_ context.Context, req common.CompletionRequest) (common.TokenCount, error) {
	var totalChars int
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			totalChars += len(block.Text)
		}
	}
	totalChars += len(req.System)

	return common.TokenCount{InputTokens: int64(totalChars / 4)}, nil
}

func (c *Client) ListModels(ctx context.Context) ([]common.ModelInfo, error) {
	page, err := c.Client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s list models: %w", c.Name, c.apiError(err))
	}

	models := make([]common.ModelInfo, 0, len(page.Data))
	for _, m := range page.Data {
		models = append(models, common.ModelInfo{
			ID:   m.ID,
			Name: m.ID,
		})
	}

	return models, nil
}

func (c *Client) buildParams(req common.CompletionRequest) (openai.ChatCompletionNewParams, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = int64(c.Model.GetMaxTokens())
	}

	msgs := toOpenAIMessages(req.Messages)
	if req.System != "" {
		msgs = append([]openai.ChatCompletionMessageParamUnion{openai.SystemMessage(req.System)}, msgs...)
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(req.Model),
		Messages: msgs,
	}

	if c.UseMaxCompletionTokens {
		params.MaxCompletionTokens = param.NewOpt(maxTokens)
	} else {
		params.MaxTokens = param.NewOpt(maxTokens)
	}

	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}

	if req.ThinkingBudget != nil {
		params.ReasoningEffort = reasoningEffortForBudget(*req.ThinkingBudget)
	}

	if len(req.Tools) > 0 {
		tools, err := toOpenAITools(req.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, fmt.Errorf("%s: %w", c.Name, err)
		}
		params.Tools = tools
	}

	return params, nil
}
