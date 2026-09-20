// Package systemone is a client for TypeSafe's System One evaluation
// endpoint (POST /v1/systemone), served by the Jev models.
//
// It implements common.SystemOne, the decision counterpart to common.LLM:
// one state plus a map of typed questions in, one calibrated typed answer per
// question out. There is no text generation, no tool loop and no streaming,
// so it does NOT implement common.LLM — the caller keeps control flow and
// combines the answers in its own code.
//
// Wire contract: https://docs.typesafe.ai/api
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
	"github.com/successr-ai/tenzing-agent-harness/pkg/providers/protocols/ratelimit"
)

const (
	// ProviderName identifies this protocol in errors and logs.
	ProviderName = "typesafe"

	// DefaultBaseURL is TypeSafe's hosted endpoint.
	DefaultBaseURL = "https://api.typesafe.ai"

	// DefaultModel is the alias for the current stable Jev release. Aliases
	// move between releases; pin a versioned id (e.g. "jev-1.13.0") once
	// confidence thresholds are tuned against one.
	DefaultModel = "jev-latest"

	evaluatePath = "/v1/systemone"
)

// Token budgets for one request, documented for jev-1.13 and not sent on the
// wire — the endpoint enforces its own. Nothing here counts tokens; these are
// the numbers a caller sizing a batch of questions plans against.
//
// A backend may advertise less: OpenRouter lists 32000 context for
// typesafe/jev-1.13, half of MaxRequestTokens. Plan against the smaller figure
// when pointing the client somewhere other than TypeSafe.
const (
	// MaxRequestTokens covers the state plus every question combined.
	MaxRequestTokens = 65536

	// MaxStateQuestionTokens covers the state plus the single longest
	// question, a second budget that binds before MaxRequestTokens when one
	// question is much larger than the rest.
	MaxStateQuestionTokens = 32768
)

// request is the wire body. The question and answer shapes are common's,
// which carry the endpoint's JSON tags, so only the envelope is mapped here.
type request struct {
	State     common.State               `json:"state"`
	Model     string                     `json:"model"`
	Questions map[string]common.Question `json:"questions"`
}

type response struct {
	Model   string                   `json:"model"`
	Answers map[string]common.Answer `json:"answers"`
	Usage   usage                    `json:"usage"`
}

type usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type ClientOption func(*clientOptions)

type clientOptions struct {
	apiKey       string
	baseURL      string
	model        string
	httpClient   *http.Client
	retryBackoff *ratelimit.RetryBackoff
}

// WithAPIKey sets the API key sent as a bearer token. Required — nothing in
// pkg/ reads environment variables.
func WithAPIKey(key string) ClientOption {
	return func(o *clientOptions) { o.apiKey = key }
}

// WithBaseURL points the client at a different host, for tests or a proxy.
func WithBaseURL(baseURL string) ClientOption {
	return func(o *clientOptions) { o.baseURL = baseURL }
}

// WithModel sets the model every request names, overriding DefaultModel. A
// request's own Model field still wins.
func WithModel(model string) ClientOption {
	return func(o *clientOptions) { o.model = model }
}

// WithHTTPClient supplies the HTTP client, for custom transports or timeouts.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(o *clientOptions) { o.httpClient = hc }
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

// Client evaluates state against typed questions. It is safe for concurrent
// use.
type Client struct {
	apiKey       string
	baseURL      string
	model        string
	httpClient   *http.Client
	retryBackoff *ratelimit.RetryBackoff
}

var _ common.SystemOne = (*Client)(nil)

// NewClient builds a client. WithAPIKey is required.
func NewClient(opts ...ClientOption) (*Client, error) {
	backoff := ratelimit.NewDefaultBackoff()
	o := &clientOptions{
		baseURL:      DefaultBaseURL,
		model:        DefaultModel,
		httpClient:   http.DefaultClient,
		retryBackoff: &backoff,
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.apiKey == "" {
		return nil, fmt.Errorf("systemone: API key is required (use WithAPIKey)")
	}
	return &Client{
		apiKey:       o.apiKey,
		baseURL:      o.baseURL,
		model:        o.model,
		httpClient:   o.httpClient,
		retryBackoff: o.retryBackoff,
	}, nil
}

// GetCurrentModel returns the model this client names in requests.
func (c *Client) GetCurrentModel() string { return c.model }

// Evaluate answers every question in req against req.State. Transient
// failures are retried with backoff.
func (c *Client) Evaluate(ctx context.Context, req common.EvaluationRequest) (common.EvaluationResponse, error) {
	if len(req.Questions) == 0 {
		return common.EvaluationResponse{}, fmt.Errorf("systemone: at least one question is required")
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	body, err := json.Marshal(request{State: req.State, Model: model, Questions: req.Questions})
	if err != nil {
		return common.EvaluationResponse{}, fmt.Errorf("systemone: encoding request: %w", err)
	}
	return c.evaluateWithRetries(ctx, body)
}

// evaluateWithRetries retries every transient failure — 408, 429, any 5xx
// (the endpoint answers 529 when overloaded), and a request that never got a
// response — matching the statuses TypeSafe's own SDKs retry. ratelimit's
// RetryOnRateLimit is not used because it retries 429 alone; its Backoff is,
// so the delays and their logging stay identical to the other protocols.
//
// ponytail: backoff only. The endpoint may send Retry-After / retry-after-ms
// on a 429 — carry the header through on a private error type if the fixed
// delays prove too blunt.
func (c *Client) evaluateWithRetries(ctx context.Context, body []byte) (common.EvaluationResponse, error) {
	cfg := c.retryBackoff.OrDefaults()
	var lastErr error
	for attempt := range cfg.MaxRetries {
		resp, err := c.evaluateOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		var apiErr *common.APIError
		if !errors.As(err, &apiErr) || !apiErr.Transient() {
			return common.EvaluationResponse{}, err
		}
		lastErr = err
		if attempt == cfg.MaxRetries-1 {
			break
		}
		if backoffErr := ratelimit.Backoff(ctx, ProviderName, attempt, cfg); backoffErr != nil {
			return common.EvaluationResponse{}, backoffErr
		}
	}
	return common.EvaluationResponse{}, fmt.Errorf("%s failed after %d attempts: %w", ProviderName, cfg.MaxRetries, lastErr)
}

func (c *Client) evaluateOnce(ctx context.Context, body []byte) (common.EvaluationResponse, error) {
	var zero common.EvaluationResponse

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+evaluatePath, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("systemone: building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return zero, &common.APIError{Provider: ProviderName, Message: "request failed", Err: err}
	}
	defer httpResp.Body.Close()

	payload, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return zero, &common.APIError{
			StatusCode: httpResp.StatusCode,
			Provider:   ProviderName,
			Message:    "reading response body",
			Err:        err,
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		return zero, &common.APIError{
			StatusCode: httpResp.StatusCode,
			Provider:   ProviderName,
			Message:    string(bytes.TrimSpace(payload)),
		}
	}

	var wire response
	if err := json.Unmarshal(payload, &wire); err != nil {
		return zero, &common.APIError{
			StatusCode: httpResp.StatusCode,
			Provider:   ProviderName,
			Message:    "decoding response",
			Err:        err,
		}
	}
	return common.EvaluationResponse{
		Model:   wire.Model,
		Answers: wire.Answers,
		Usage: common.Usage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
		},
	}, nil
}
