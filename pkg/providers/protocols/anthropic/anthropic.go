package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

const (
	// anthropicBaseURL is the base URL for the Anthropic Model API.
	anthropicBaseURL = "https://api.anthropic.com"

	// nonStreamingCap is the largest max_tokens the SDK accepts on a
	// non-streaming request: its guard rejects requests expected to take longer
	// than 10 minutes, scaled at 128000 tokens per hour.
	nonStreamingCap int64 = 128000 / 6
)

type Model = common.Model

// maxNonStreamingTokens returns the largest max_tokens the SDK permits for a
// non-streaming request to the given model, honoring the SDK's per-model
// limits on top of the 10-minute guard.
func maxNonStreamingTokens(model string) int64 {
	limit := nonStreamingCap
	if modelLimit, ok := constant.ModelNonStreamingTokens[model]; ok {
		limit = min(limit, int64(modelLimit))
	}
	return limit
}

type Client struct {
	client *anthropicSDK.Client
	model  Model
	// retryBackoff, when non-nil, retries requests that fail with HTTP 429
	// using exponential backoff. NewClient always sets it; nil (tests only)
	// disables 429 retries.
	retryBackoff *ratelimit.RetryBackoff
}

type ClientOption func(*clientOptions)

type clientOptions struct {
	model  Model
	apiKey string
	// retryBackoff configures client-side 429 retries; defaults to
	// ratelimit.NewDefaultBackoff.
	retryBackoff *ratelimit.RetryBackoff
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

// WithAPIKey sets the API key sent to the Anthropic API.
func WithAPIKey(key string) ClientOption {
	return func(o *clientOptions) {
		o.apiKey = key
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
// input token count. Without it the client is unlimited.
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

// NewClient creates an Anthropic LLM client. Model is required; there is no
// default.
func NewClient(model Model, options ...ClientOption) (common.LLM, error) {
	opts := loadClientOptions(model, options)
	if opts.model == nil {
		return nil, fmt.Errorf("anthropic: Model is required")
	}

	reqOpts := []option.RequestOption{option.WithBaseURL(anthropicBaseURL)}
	if opts.apiKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(opts.apiKey))
	}
	client := anthropicSDK.NewClient(reqOpts...)

	raw := &Client{
		client:       &client,
		model:        opts.model,
		retryBackoff: opts.retryBackoff,
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
		return nil, errors.New("anthropic: rate limit requires positive rate and burst size")
	case opts.rateLimit.MaxConcurrency > 0:
		llm = ratelimit.Wrap(llm, ratelimit.NewSemaphore(int(opts.rateLimit.MaxConcurrency)), ratelimit.CostPerRequest)
	}
	return llm, nil
}

// apiError wraps an SDK or transport error into *common.APIError so callers
// can classify it with errors.As. Status 0 means no HTTP response arrived.
func apiError(err error) *common.APIError {
	status := 0
	var sdkErr *anthropicSDK.Error
	if errors.As(err, &sdkErr) {
		status = sdkErr.StatusCode
	}
	return &common.APIError{
		StatusCode: status,
		Provider:   "anthropic",
		Message:    err.Error(),
		Err:        err,
	}
}

func (a *Client) SendSyncMessage(ctx context.Context, req common.CompletionRequest) (common.CompletionResponse, error) {
	params, err := a.buildParams(req)
	if err != nil {
		return common.CompletionResponse{}, err
	}
	params.MaxTokens = min(params.MaxTokens, maxNonStreamingTokens(req.Model))

	return ratelimit.RetryOnRateLimit(ctx, "anthropic", a.retryBackoff, func() (common.CompletionResponse, error) {
		res, err := a.client.Messages.New(ctx, params)
		if err != nil {
			return common.CompletionResponse{}, fmt.Errorf("anthropic send message: %w", apiError(err))
		}
		return fromAnthropicResponse(res), nil
	})
}

// SendStreamingMessage streams a completion. The events channel is always
// closed before returning, including on error. Rate-limited attempts are
// retried only if no events have been emitted yet, so consumers never see
// duplicated deltas.
func (a *Client) SendStreamingMessage(ctx context.Context, req common.CompletionRequest, events chan<- common.StreamEvent) error {
	defer close(events)

	params, err := a.buildParams(req)
	if err != nil {
		return err
	}

	err = ratelimit.RetryStreaming(ctx, "anthropic", a.retryBackoff, func() (bool, error) {
		emitted, err := a.streamOnce(ctx, params, events)
		if err != nil {
			return emitted, apiError(err)
		}
		return emitted, nil
	})
	if err != nil {
		events <- common.StreamEvent{Type: common.StreamEventError, Err: err}
		return fmt.Errorf("anthropic streaming: %w", err)
	}
	return nil
}

// streamOnce runs a single streaming attempt. It reports whether any events
// were emitted so the caller knows if a retry is safe.
func (a *Client) streamOnce(ctx context.Context, params anthropicSDK.MessageNewParams, events chan<- common.StreamEvent) (bool, error) {
	stream := a.client.Messages.NewStreaming(ctx, params)

	var accumulated common.CompletionResponse
	// Content blocks under construction, keyed by stream index. Tool input
	// JSON arrives as partial fragments that must be buffered until
	// content_block_stop.
	blocks := map[int64]*common.ContentBlock{}
	jsonParts := map[int64][]string{}
	emitted := false

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			accumulated.ID = event.Message.ID
			accumulated.Model = event.Message.Model
			accumulated.Usage.InputTokens = event.Message.Usage.InputTokens
			accumulated.Usage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
			accumulated.Usage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
			events <- common.StreamEvent{Type: common.StreamEventStart}
			emitted = true

		case "content_block_start":
			switch event.ContentBlock.Type {
			case "text":
				blocks[event.Index] = &common.ContentBlock{Type: common.ContentTypeText}
			case "tool_use":
				blocks[event.Index] = &common.ContentBlock{
					Type:      common.ContentTypeToolUse,
					ToolUseID: event.ContentBlock.ID,
					ToolName:  event.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if block := blocks[event.Index]; block != nil {
					block.Text += event.Delta.Text
				}
				events <- common.StreamEvent{
					Type: common.StreamEventDelta,
					Text: event.Delta.Text,
				}
			case "input_json_delta":
				jsonParts[event.Index] = append(jsonParts[event.Index], event.Delta.PartialJSON)
			}

		case "content_block_stop":
			block := blocks[event.Index]
			if block == nil {
				continue
			}
			if block.Type == common.ContentTypeToolUse {
				input := strings.Join(jsonParts[event.Index], "")
				if input == "" {
					input = "{}"
				}
				block.ToolInput = json.RawMessage(input)
			}
			accumulated.Content = append(accumulated.Content, *block)

		case "message_delta":
			accumulated.StopReason = fromAnthropicStopReason(event.Delta.StopReason)
			accumulated.Usage.OutputTokens = event.Usage.OutputTokens
		}
	}

	if err := stream.Err(); err != nil {
		return emitted, err
	}

	// Emitted after the loop rather than on message_stop so consumers always
	// get a stop event when the stream ends cleanly.
	events <- common.StreamEvent{
		Type:     common.StreamEventStop,
		Response: &accumulated,
	}

	return emitted, nil
}

// SendMessageWithTools sends a completion request with the given tools,
// overriding any tools already set on the request.
func (a *Client) SendMessageWithTools(ctx context.Context, req common.CompletionRequest, tools []common.ToolDefinition) (common.CompletionResponse, error) {
	req.Tools = tools
	params, err := a.buildParams(req)
	if err != nil {
		return common.CompletionResponse{}, err
	}
	params.MaxTokens = min(params.MaxTokens, maxNonStreamingTokens(req.Model))

	return ratelimit.RetryOnRateLimit(ctx, "anthropic", a.retryBackoff, func() (common.CompletionResponse, error) {
		res, err := a.client.Messages.New(ctx, params)
		if err != nil {
			return common.CompletionResponse{}, fmt.Errorf("anthropic send message with tools: %w", apiError(err))
		}
		return fromAnthropicResponse(res), nil
	})
}

func (a *Client) GetCurrentModel() string {
	return a.model.GetName()
}

func (a *Client) GetContextWindowSize() int {
	return a.model.GetContextWindowSize()
}

func (a *Client) GetModel() common.Model {
	return a.model
}

func (a *Client) CountTokens(ctx context.Context, req common.CompletionRequest) (common.TokenCount, error) {
	params := anthropicSDK.MessageCountTokensParams{
		Model:    anthropicSDK.Model(req.Model),
		Messages: toAnthropicMessages(req.Messages),
	}

	if req.System != "" {
		params.System = anthropicSDK.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: []anthropicSDK.TextBlockParam{
				{Text: req.System},
			},
		}
	}

	res, err := a.client.Messages.CountTokens(ctx, params)
	if err != nil {
		return common.TokenCount{}, fmt.Errorf("anthropic count tokens: %w", apiError(err))
	}

	return common.TokenCount{InputTokens: res.InputTokens}, nil
}

func (a *Client) ListModels(ctx context.Context) ([]common.ModelInfo, error) {
	page, err := a.client.Models.List(ctx, anthropicSDK.ModelListParams{})
	if err != nil {
		return nil, fmt.Errorf("anthropic list models: %w", apiError(err))
	}

	models := make([]common.ModelInfo, 0, len(page.Data))
	for _, m := range page.Data {
		models = append(models, common.ModelInfo{
			ID:        m.ID,
			Name:      m.DisplayName,
			MaxTokens: m.MaxTokens,
		})
	}

	return models, nil
}

func (a *Client) buildParams(req common.CompletionRequest) (anthropicSDK.MessageNewParams, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = int64(a.model.GetMaxTokens())
	}

	params := anthropicSDK.MessageNewParams{
		Model:     anthropicSDK.Model(req.Model),
		MaxTokens: maxTokens,
		Messages:  toAnthropicMessages(req.Messages),
	}

	if req.System != "" {
		params.System = []anthropicSDK.TextBlockParam{
			{Text: req.System},
		}
	}

	if req.Temperature != nil {
		params.Temperature = anthropicSDK.Float(*req.Temperature)
	}

	if req.ThinkingBudget != nil {
		if req.Think == nil || !*req.Think {
			return anthropicSDK.MessageNewParams{}, fmt.Errorf("anthropic: ThinkingBudget requires Think=true")
		}
		// The API rejects budgets below 1024; clamp up rather than fail.
		budget := max(*req.ThinkingBudget, 1024)
		if budget >= maxTokens {
			return anthropicSDK.MessageNewParams{}, fmt.Errorf("anthropic: ThinkingBudget (%d) must be less than MaxTokens (%d)", budget, maxTokens)
		}
		params.Thinking = anthropicSDK.ThinkingConfigParamOfEnabled(budget)
	}

	if len(req.Tools) > 0 {
		tools, err := toAnthropicTools(req.Tools)
		if err != nil {
			return anthropicSDK.MessageNewParams{}, err
		}
		params.Tools = tools
	}

	// A cache_control breakpoint on the last system block and last tool caches
	// everything up to and including them (tools precede system in the prompt).
	if req.CacheSystemAndTools {
		cc := anthropicSDK.NewCacheControlEphemeralParam()
		if n := len(params.System); n > 0 {
			params.System[n-1].CacheControl = cc
		}
		if n := len(params.Tools); n > 0 && params.Tools[n-1].OfTool != nil {
			params.Tools[n-1].OfTool.CacheControl = cc
		}
	}

	return params, nil
}

func toAnthropicMessages(msgs []common.Message) []anthropicSDK.MessageParam {
	result := make([]anthropicSDK.MessageParam, 0, len(msgs))
	for _, msg := range msgs {
		blocks := toAnthropicContentBlocks(msg.Content)
		switch msg.Role {
		case common.RoleUser:
			result = append(result, anthropicSDK.NewUserMessage(blocks...))
		case common.RoleAssistant:
			result = append(result, anthropicSDK.NewAssistantMessage(blocks...))
		case common.RoleTool:
			// Anthropic has no tool role; tool_result blocks ride in a
			// user message.
			result = append(result, anthropicSDK.NewUserMessage(blocks...))
		case common.RoleSystem:
			continue
		}
	}
	return result
}

func toAnthropicContentBlocks(blocks []common.ContentBlock) []anthropicSDK.ContentBlockParamUnion {
	result := make([]anthropicSDK.ContentBlockParamUnion, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case common.ContentTypeText:
			result = append(result, anthropicSDK.NewTextBlock(block.Text))
		case common.ContentTypeToolUse:
			result = append(result, anthropicSDK.NewToolUseBlock(block.ToolUseID, block.ToolInput, block.ToolName))
		case common.ContentTypeToolResult:
			result = append(result, anthropicSDK.NewToolResultBlock(block.ToolResultID, block.ToolOutput, false))
		case common.ContentTypeImage:
			if block.Image == nil {
				continue
			}
			result = append(result, anthropicSDK.NewImageBlockBase64(block.Image.MediaType, block.Image.Data))
		}
	}
	return result
}

func toAnthropicTools(tools []common.ToolDefinition) ([]anthropicSDK.ToolUnionParam, error) {
	result := make([]anthropicSDK.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		// InputSchema is a full JSON Schema object; Anthropic's ToolInputSchemaParam
		// wants its properties and required fields split out.
		var schema struct {
			Properties json.RawMessage `json:"properties"`
			Required   []string        `json:"required"`
		}
		if tool.InputSchema != nil {
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("anthropic tool %q: parse input schema: %w", tool.Name, err)
			}
		}

		var props any
		if schema.Properties != nil {
			if err := json.Unmarshal(schema.Properties, &props); err != nil {
				return nil, fmt.Errorf("anthropic tool %q: parse schema properties: %w", tool.Name, err)
			}
		}

		result = append(result, anthropicSDK.ToolUnionParam{
			OfTool: &anthropicSDK.ToolParam{
				Name:        tool.Name,
				Description: anthropicSDK.String(tool.Description),
				InputSchema: anthropicSDK.ToolInputSchemaParam{
					Properties: props,
					Required:   schema.Required,
				},
			},
		})
	}
	return result, nil
}

func fromAnthropicResponse(res *anthropicSDK.Message) common.CompletionResponse {
	content := make([]common.ContentBlock, 0, len(res.Content))
	for _, block := range res.Content {
		switch block.Type {
		case "text":
			content = append(content, common.NewTextContent(block.Text))
		case "tool_use":
			content = append(content, common.NewToolUseContent(block.ID, block.Name, block.Input))
		}
	}

	return common.CompletionResponse{
		ID:         res.ID,
		Content:    content,
		StopReason: fromAnthropicStopReason(res.StopReason),
		Usage: common.Usage{
			InputTokens:              res.Usage.InputTokens,
			OutputTokens:             res.Usage.OutputTokens,
			CacheReadInputTokens:     res.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: res.Usage.CacheCreationInputTokens,
		},
		Model: res.Model,
	}
}

func fromAnthropicStopReason(reason anthropicSDK.StopReason) common.StopReason {
	switch reason {
	case anthropicSDK.StopReasonEndTurn:
		return common.StopReasonEndTurn
	case anthropicSDK.StopReasonMaxTokens:
		return common.StopReasonMaxTokens
	case anthropicSDK.StopReasonToolUse:
		return common.StopReasonToolUse
	default:
		return common.StopReason(reason)
	}
}
