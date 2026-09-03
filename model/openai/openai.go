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

const defaultBaseURL = "https://api.openai.com/v1"

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
}

// Client is an OpenAI Chat Completions implementation of model.Model.
type Client struct {
	apiKey       string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
}

// New creates a Client from cfg.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{
		apiKey:       cfg.APIKey,
		baseURL:      baseURL,
		defaultModel: cfg.DefaultModel,
		httpClient:   hc,
	}
}

// Chat performs a single chat completion request.
func (c *Client) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = c.defaultModel
	}
	payload := struct {
		Model       string            `json:"model"`
		Messages    []model.Message   `json:"messages"`
		Tools       []model.ToolParam `json:"tools,omitempty"`
		Temperature *float64          `json:"temperature,omitempty"`
		MaxTokens   int               `json:"max_tokens,omitempty"`
	}{
		Model:       modelName,
		Messages:    req.Messages,
		Tools:       req.Tools,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: unexpected status %d: %s", resp.StatusCode, truncate(data, 512))
	}

	var wire struct {
		Choices []struct {
			Message      model.Message `json:"message"`
			FinishReason string        `json:"finish_reason"`
		} `json:"choices"`
		Usage model.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(wire.Choices) == 0 {
		return nil, fmt.Errorf("openai: response contains no choices")
	}

	return &model.Response{
		Message:      wire.Choices[0].Message,
		FinishReason: wire.Choices[0].FinishReason,
		Usage:        wire.Usage,
	}, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
