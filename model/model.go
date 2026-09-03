// Package model defines the provider-agnostic chat model interface and the
// message / tool-call types shared by the rest of the SDK.
package model

import (
	"context"
	"encoding/json"
)

// Role identifies the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// FunctionCall describes a single function invocation requested by the model.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON encoded by the model
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// Message is one turn in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// FunctionDef is the schema of a callable function exposed to the model.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ToolParam wraps a tool definition as sent in a chat request.
type ToolParam struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// Usage reports token consumption of a single model call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Request is a single chat completion request.
type Request struct {
	Model       string      `json:"model"`
	Messages    []Message   `json:"messages"`
	Tools       []ToolParam `json:"tools,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
}

// Response is the outcome of a single chat completion request.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Model is implemented by chat providers (OpenAI, Anthropic, local models...).
type Model interface {
	Chat(ctx context.Context, req *Request) (*Response, error)
}
