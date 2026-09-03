// Package model defines the provider-agnostic chat model interface and the
// message / tool-call types shared by the rest of the SDK.
//
// These types double as the Chat Completions wire format, so they are a
// frozen compatibility surface: additive changes only.
package model

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Index orders tool calls inside streaming deltas (used by StreamModel).
	Index int `json:"index,omitempty"`
}

// ImageURL is the payload of an image content part.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto" | "low" | "high"
}

// Content part kinds understood by every adapter.
const (
	PartText     = "text"
	PartImageURL = "image_url"
)

// ContentPart is one part of a multimodal message body. "text" and
// "image_url" are first-class; any provider-specific kind is round-tripped
// verbatim through Extra so nothing is lost between parse and send.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	// Extra holds the raw JSON object for kinds not covered above.
	Extra json.RawMessage `json:"-"`
}

func (p ContentPart) MarshalJSON() ([]byte, error) {
	if len(p.Extra) > 0 {
		return json.Marshal(json.RawMessage(p.Extra))
	}
	switch p.Type {
	case PartText:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{p.Type, p.Text})
	case PartImageURL:
		return json.Marshal(struct {
			Type     string    `json:"type"`
			ImageURL *ImageURL `json:"image_url"`
		}{p.Type, p.ImageURL})
	}
	return nil, fmt.Errorf("model: unknown content part type %q", p.Type)
}

func (p *ContentPart) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	p.Type = probe.Type
	switch probe.Type {
	case PartText:
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		p.Text, p.ImageURL, p.Extra = v.Text, nil, nil
	case PartImageURL:
		var v struct {
			ImageURL *ImageURL `json:"image_url"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		p.ImageURL, p.Text, p.Extra = v.ImageURL, "", nil
	default:
		p.Text, p.ImageURL = "", nil
		p.Extra = append(json.RawMessage(nil), data...)
	}
	return nil
}

// Message is one turn in a conversation.
//
// Content carries plain text; Parts carries a multimodal body. When Parts is
// non-empty it is serialized as a content-part array and Content is ignored,
// otherwise Content is serialized as a plain string.
type Message struct {
	Role       Role
	Content    string
	Parts      []ContentPart
	Name       string
	ToolCalls  []ToolCall
	ToolCallID string
}

func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       Role       `json:"role"`
		Content    any        `json:"content,omitempty"`
		Name       string     `json:"name,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	w := wire{Role: m.Role, Name: m.Name, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	switch {
	case len(m.Parts) > 0:
		w.Content = m.Parts
	case m.Content != "" || m.Role == RoleTool:
		// Tool results must always carry a content field: several strict
		// OpenAI-compatible backends reject tool messages without one.
		w.Content = m.Content
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type wire struct {
		Role       Role            `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*m = Message{Role: w.Role, Name: w.Name, ToolCalls: w.ToolCalls, ToolCallID: w.ToolCallID}
	if len(w.Content) > 0 && string(w.Content) != "null" {
		var text string
		if err := json.Unmarshal(w.Content, &text); err == nil {
			m.Content = text
		} else {
			var parts []ContentPart
			if err := json.Unmarshal(w.Content, &parts); err != nil {
				return fmt.Errorf("model: message content: %w", err)
			}
			m.Parts = parts
		}
	}
	return nil
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
	PromptTokens            int           `json:"prompt_tokens"`
	CompletionTokens        int           `json:"completion_tokens"`
	TotalTokens             int           `json:"total_tokens"`
	PromptTokensDetails     *TokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *TokenDetails `json:"completion_tokens_details,omitempty"`
}

// TokenDetails breaks out provider-reported token sub-counts.
type TokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// Accumulate adds o into u, for totals across turns.
func (u *Usage) Accumulate(o Usage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.TotalTokens += o.TotalTokens
	if d := o.PromptTokensDetails; d != nil {
		if u.PromptTokensDetails == nil {
			u.PromptTokensDetails = &TokenDetails{}
		}
		u.PromptTokensDetails.CachedTokens += d.CachedTokens
		u.PromptTokensDetails.AudioTokens += d.AudioTokens
	}
	if d := o.CompletionTokensDetails; d != nil {
		if u.CompletionTokensDetails == nil {
			u.CompletionTokensDetails = &TokenDetails{}
		}
		u.CompletionTokensDetails.CachedTokens += d.CachedTokens
		u.CompletionTokensDetails.AudioTokens += d.AudioTokens
	}
}

// Settings groups optional sampling and decoding parameters. It is embedded
// in Request so its JSON keys stay flat on the wire.
type Settings struct {
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	Stop              []string        `json:"stop,omitempty"`
	Seed              *int64          `json:"seed,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ToolChoice        *ToolChoice     `json:"tool_choice,omitempty"`
	ResponseFormat    *ResponseFormat `json:"response_format,omitempty"`
}

// ToolChoice controls whether and which tool the model must call. Construct
// with ToolChoiceAuto / ToolChoiceNone / ToolChoiceRequired /
// ToolChoiceFunction.
type ToolChoice struct {
	mode     string
	function string
}

func ToolChoiceAuto() *ToolChoice     { return &ToolChoice{mode: "auto"} }
func ToolChoiceNone() *ToolChoice     { return &ToolChoice{mode: "none"} }
func ToolChoiceRequired() *ToolChoice { return &ToolChoice{mode: "required"} }

func ToolChoiceFunction(name string) *ToolChoice {
	return &ToolChoice{mode: "function", function: name}
}

func (t ToolChoice) MarshalJSON() ([]byte, error) {
	if t.function != "" {
		return json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": t.function},
		})
	}
	mode := t.mode
	if mode == "" {
		mode = "auto"
	}
	return json.Marshal(mode)
}

// Response format types.
const (
	FormatText       = "text"
	FormatJSONObject = "json_object"
	FormatJSONSchema = "json_schema"
)

// ResponseFormat constrains the shape of model output.
type ResponseFormat struct {
	Type       string      `json:"type"` // FormatText | FormatJSONObject | FormatJSONSchema
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema carries a strict-schema response format.
type JSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      bool            `json:"strict"`
}

// Request is a single chat completion request. Adapters serialize it
// directly, so its JSON layout is the wire contract.
type Request struct {
	Model    string      `json:"model"`
	Messages []Message   `json:"messages"`
	Tools    []ToolParam `json:"tools,omitempty"`
	Settings
}

// Response is the outcome of a single chat completion request.
type Response struct {
	ID           string
	Model        string
	Message      Message
	FinishReason string
	Usage        Usage
}

// Model is implemented by chat providers (OpenAI, Anthropic, local models...).
type Model interface {
	Chat(ctx context.Context, req *Request) (*Response, error)
}

// ModelFunc adapts a plain function to the Model interface.
type ModelFunc func(ctx context.Context, req *Request) (*Response, error)

// Chat implements Model.
func (f ModelFunc) Chat(ctx context.Context, req *Request) (*Response, error) {
	return f(ctx, req)
}
