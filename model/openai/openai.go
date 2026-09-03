// Package openai implements model.Model against the OpenAI Chat Completions
// API. Any OpenAI-compatible endpoint (vLLM, Ollama, Qwen, ...) can be used by
// overriding Config.BaseURL.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/example/agent-sdk/model"
)

const (
	provider           = "openai"
	defaultBaseURL     = "https://api.openai.com/v1"
	maxResponseBytes   = 64 << 20
	defaultHTTPTimeout = 5 * time.Minute
)

// Config configures the OpenAI client.
type Config struct {
	// APIKey is the bearer token sent with every request.
	APIKey string
	// BaseURL overrides the API endpoint. Defaults to https://api.openai.com/v1.
	BaseURL string
	// DefaultModel is used when a Request carries an empty model name.
	DefaultModel string
	// HTTPClient overrides the HTTP client used for requests.
	HTTPClient *http.Client
	// DisableStreamUsage stops the client from sending
	// stream_options.include_usage on streaming calls. Enable it for
	// backends that reject the parameter; Usage on streams will then be
	// zero.
	DisableStreamUsage bool
}

// Client is an OpenAI Chat Completions implementation of model.Model and
// model.StreamModel.
type Client struct {
	apiKey             string
	baseURL            string
	defaultModel       string
	httpClient         *http.Client
	disableStreamUsage bool
}

var _ model.StreamModel = (*Client)(nil)

// New creates a Client from cfg.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		apiKey:             cfg.APIKey,
		baseURL:            baseURL,
		defaultModel:       cfg.DefaultModel,
		httpClient:         hc,
		disableStreamUsage: cfg.DisableStreamUsage,
	}
}

// Chat performs a single chat completion request. All failures are returned
// as *model.ModelError, except context.Canceled which passes through
// unchanged.
func (c *Client) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	if req == nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("nil request")}
	}
	wire := *req
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if wire.Model == "" {
		return nil, &model.ModelError{
			Kind:     model.ErrorInvalidRequest,
			Provider: provider,
			Err:      fmt.Errorf("no model specified in request or Config.DefaultModel"),
		}
	}

	body, err := json.Marshal(&wire)
	if err != nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("marshal request: %w", err)}
	}

	httpReq, err := c.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, model.ClassifyTransportError(provider, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, model.ClassifyTransportError(provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, model.NewHTTPError(provider, resp.StatusCode, string(data))
	}

	var parsed struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message      model.Message `json:"message"`
			FinishReason string        `json:"finish_reason"`
		} `json:"choices"`
		Usage model.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, &model.ModelError{Kind: model.ErrorProtocol, Provider: provider, Body: truncate(data, 512), Err: fmt.Errorf("decode response: %w", err)}
	}
	if len(parsed.Choices) == 0 {
		return nil, &model.ModelError{Kind: model.ErrorProtocol, Provider: provider, Body: truncate(data, 512), Err: fmt.Errorf("response contains no choices")}
	}

	return &model.Response{
		ID:           parsed.ID,
		Model:        parsed.Model,
		Message:      parsed.Choices[0].Message,
		FinishReason: parsed.Choices[0].FinishReason,
		Usage:        parsed.Usage,
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// newHTTPRequest builds the authenticated POST for a marshaled payload.
func (c *Client) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}
