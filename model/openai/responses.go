package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model/sse"
)

type responsesRequest struct {
	Model             string               `json:"model"`
	Input             []responsesInputItem `json:"input"`
	Tools             []responsesTool      `json:"tools,omitempty"`
	Stream            bool                 `json:"stream,omitempty"`
	Temperature       *float64             `json:"temperature,omitempty"`
	TopP              *float64             `json:"top_p,omitempty"`
	MaxOutputTokens   int                  `json:"max_output_tokens,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	ToolChoice        json.RawMessage      `json:"tool_choice,omitempty"`
}

type responsesInputItem struct {
	Type      string             `json:"type"`
	Role      model.Role         `json:"role,omitempty"`
	Content   []responsesContent `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Output    string             `json:"output,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}

type responsesContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (c *Client) responsesChat(ctx context.Context, req *model.Request) (*model.Response, error) {
	wire, err := c.newResponsesRequest(req, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("marshal responses request: %w", err)}
	}
	httpReq, err := c.newJSONRequest(ctx, "/responses", body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, model.ClassifyTransportError(provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, model.ClassifyTransportError(provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, model.NewHTTPError(provider, resp.StatusCode, string(data))
	}
	var parsed responsesPayload
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, &model.ModelError{Kind: model.ErrorProtocol, Provider: provider, Body: truncate(data, 512), Err: fmt.Errorf("decode responses response: %w", err)}
	}
	return parsed.toResponse()
}

func (c *Client) responsesChatStream(ctx context.Context, req *model.Request) (model.StreamReader, error) {
	wire, err := c.newResponsesRequest(req, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("marshal responses request: %w", err)}
	}
	httpReq, err := c.newJSONRequest(ctx, "/responses", body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, model.ClassifyTransportError(provider, err)
	}
	if resp.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, model.ClassifyTransportError(provider, readErr)
		}
		return nil, model.NewHTTPError(provider, resp.StatusCode, string(data))
	}
	return &responsesStreamReader{
		provider: provider,
		resp:     resp,
		dec:      sse.NewDecoder(resp.Body),
		calls:    make(map[int]responsesCall),
		emitted:  make(map[int]bool),
		argsSeen: make(map[int]bool),
	}, nil
}

func (c *Client) newResponsesRequest(req *model.Request, stream bool) (responsesRequest, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = c.defaultModel
	}
	if modelName == "" {
		return responsesRequest{}, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: errors.New("no model specified in request or Config.DefaultModel")}
	}
	input, err := responsesInput(req.Messages)
	if err != nil {
		return responsesRequest{}, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: err}
	}
	tools := make([]responsesTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Type != "function" {
			return responsesRequest{}, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("unsupported responses tool type %q", t.Type)}
		}
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	wire := responsesRequest{
		Model:             modelName,
		Input:             input,
		Tools:             tools,
		Stream:            stream,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		MaxOutputTokens:   req.MaxTokens,
		ParallelToolCalls: req.ParallelToolCalls,
	}
	if req.ToolChoice != nil {
		choice, err := json.Marshal(req.ToolChoice)
		if err != nil {
			return responsesRequest{}, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: fmt.Errorf("marshal tool choice: %w", err)}
		}
		wire.ToolChoice = choice
	}
	return wire, nil
}

func responsesInput(messages []model.Message) ([]responsesInputItem, error) {
	items := make([]responsesInputItem, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleTool:
			if msg.ToolCallID == "" {
				return nil, errors.New("tool message has no tool_call_id")
			}
			items = append(items, responsesInputItem{Type: "function_call_output", CallID: msg.ToolCallID, Output: msg.Content})
		case model.RoleSystem, model.RoleUser, model.RoleAssistant:
			if msg.Content != "" || len(msg.Parts) > 0 {
				content, err := responsesContentFor(msg)
				if err != nil {
					return nil, err
				}
				items = append(items, responsesInputItem{Type: "message", Role: msg.Role, Content: content})
			}
			if msg.Role == model.RoleAssistant {
				for _, call := range msg.ToolCalls {
					items = append(items, responsesInputItem{Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
				}
			}
		default:
			return nil, fmt.Errorf("unsupported message role %q for responses", msg.Role)
		}
	}
	return items, nil
}

func responsesContentFor(msg model.Message) ([]responsesContent, error) {
	textType := "input_text"
	if msg.Role == model.RoleAssistant {
		textType = "output_text"
	}
	if len(msg.Parts) == 0 {
		return []responsesContent{{Type: textType, Text: msg.Content}}, nil
	}
	content := make([]responsesContent, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case model.PartText:
			content = append(content, responsesContent{Type: textType, Text: part.Text})
		case model.PartImageURL:
			if msg.Role == model.RoleAssistant || part.ImageURL == nil {
				return nil, errors.New("responses image content must be a user or system image with a URL")
			}
			content = append(content, responsesContent{Type: "input_image", ImageURL: part.ImageURL.URL})
		default:
			return nil, fmt.Errorf("unsupported responses content part %q", part.Type)
		}
	}
	return content, nil
}

type responsesPayload struct {
	ID     string                `json:"id"`
	Model  string                `json:"model"`
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage        `json:"usage"`
	Error  *responsesError       `json:"error"`
}

type responsesError struct {
	Message string `json:"message"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesOutputItem struct {
	Type      string             `json:"type"`
	ID        string             `json:"id"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Role      model.Role         `json:"role"`
	Content   []responsesContent `json:"content"`
}

func (p responsesPayload) toResponse() (*model.Response, error) {
	if p.Error != nil {
		return nil, &model.ModelError{Kind: model.ErrorProtocol, Provider: provider, Err: errors.New(p.Error.Message)}
	}
	var text strings.Builder
	var calls []model.ToolCall
	for index, item := range p.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" || content.Type == "text" {
					text.WriteString(content.Text)
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			calls = append(calls, model.ToolCall{ID: id, Type: "function", Index: index, Function: model.FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		}
	}
	finishReason := "stop"
	if len(calls) > 0 {
		finishReason = "tool_calls"
	}
	if p.Status != "" && p.Status != "completed" {
		finishReason = p.Status
	}
	return &model.Response{
		ID:    p.ID,
		Model: p.Model,
		Message: model.Message{
			Role:      model.RoleAssistant,
			Content:   text.String(),
			ToolCalls: calls,
		},
		FinishReason: finishReason,
		Usage: model.Usage{
			PromptTokens:     p.Usage.InputTokens,
			CompletionTokens: p.Usage.OutputTokens,
			TotalTokens:      p.Usage.TotalTokens,
		},
	}, nil
}

type responsesCall struct {
	id   string
	name string
}

type responsesStreamReader struct {
	provider string
	resp     *http.Response
	dec      *sse.Decoder

	queue []model.StreamEvent
	cur   model.StreamEvent
	err   error

	calls    map[int]responsesCall
	emitted  map[int]bool
	argsSeen map[int]bool

	finishSeen   bool
	finishReason string
	usage        model.Usage
	id, mdl      string
	finished     bool
}

func (s *responsesStreamReader) Next() bool {
	if s.err != nil || s.finished {
		return false
	}
	for {
		if len(s.queue) > 0 {
			s.cur = s.queue[0]
			s.queue = s.queue[1:]
			return true
		}
		ev, err := s.dec.Next()
		switch {
		case err == io.EOF:
			if !s.finishSeen {
				s.err = &model.ModelError{Kind: model.ErrorProtocol, Provider: s.provider, Err: errors.New("responses stream ended before response.completed")}
				return false
			}
			s.emitFinish()
		case err != nil:
			s.err = model.ClassifyTransportError(s.provider, err)
			return false
		default:
			data := strings.TrimSpace(ev.Data)
			if data == "" || data == doneSentinel {
				continue
			}
			if !s.handleEvent(data) {
				return false
			}
		}
	}
}

func (s *responsesStreamReader) handleEvent(data string) bool {
	var event struct {
		Type        string              `json:"type"`
		Delta       string              `json:"delta"`
		OutputIndex int                 `json:"output_index"`
		Item        responsesOutputItem `json:"item"`
		Response    responsesPayload    `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		s.err = &model.ModelError{Kind: model.ErrorProtocol, Provider: s.provider, Body: truncate([]byte(data), 512), Err: fmt.Errorf("decode responses stream event: %w", err)}
		return false
	}
	index := event.OutputIndex
	switch event.Type {
	case "response.output_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, model.StreamEvent{Type: model.StreamTextDelta, Text: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, model.StreamEvent{Type: model.StreamReasoningDelta, Reasoning: event.Delta})
		}
	case "response.output_item.added":
		s.addFunctionCall(index, event.Item, "")
	case "response.function_call_arguments.delta":
		s.addFunctionCall(index, event.Item, event.Delta)
		if event.Delta != "" {
			s.argsSeen[index] = true
		}
	case "response.output_item.done", "response.function_call_arguments.done":
		arguments := event.Item.Arguments
		s.addFunctionCall(index, event.Item, "")
		if arguments != "" && !s.argsSeen[index] {
			call := s.callFor(index, event.Item)
			s.queue = append(s.queue, model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{ID: call.id, Type: "function", Index: index, Function: model.FunctionCall{Name: call.name, Arguments: arguments}}})
			s.argsSeen[index] = true
		}
	case "response.completed":
		if event.Response.Error != nil {
			s.err = &model.ModelError{Kind: model.ErrorProtocol, Provider: s.provider, Err: errors.New(event.Response.Error.Message)}
			return false
		}
		s.id = event.Response.ID
		s.mdl = event.Response.Model
		s.usage = model.Usage{PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens, TotalTokens: event.Response.Usage.TotalTokens}
		s.finishReason = "stop"
		for _, item := range event.Response.Output {
			if item.Type == "function_call" {
				s.finishReason = "tool_calls"
				break
			}
		}
		s.finishSeen = true
		s.emitFinish()
	case "response.failed":
		message := "responses stream failed"
		if event.Response.Error != nil && event.Response.Error.Message != "" {
			message = event.Response.Error.Message
		}
		s.err = &model.ModelError{Kind: model.ErrorProtocol, Provider: s.provider, Err: errors.New(message)}
		return false
	}
	return true
}

func (s *responsesStreamReader) addFunctionCall(index int, item responsesOutputItem, arguments string) {
	if item.Type != "" && item.Type != "function_call" {
		return
	}
	call := s.callFor(index, item)
	if !s.emitted[index] && (call.id != "" || call.name != "" || arguments != "") {
		s.queue = append(s.queue, model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{ID: call.id, Type: "function", Index: index, Function: model.FunctionCall{Name: call.name, Arguments: arguments}}})
		s.emitted[index] = true
		return
	}
	if arguments != "" {
		s.queue = append(s.queue, model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{ID: call.id, Type: "function", Index: index, Function: model.FunctionCall{Name: call.name, Arguments: arguments}}})
	}
}

func (s *responsesStreamReader) callFor(index int, item responsesOutputItem) responsesCall {
	call := s.calls[index]
	if item.CallID != "" {
		call.id = item.CallID
	} else if item.ID != "" && call.id == "" {
		call.id = item.ID
	}
	if item.Name != "" {
		call.name = item.Name
	}
	s.calls[index] = call
	return call
}

func (s *responsesStreamReader) emitFinish() {
	if s.finished {
		return
	}
	s.finished = true
	s.queue = append(s.queue, model.StreamEvent{Type: model.StreamFinish, FinishReason: s.finishReason, Usage: s.usage, ID: s.id, Model: s.mdl})
}

func (s *responsesStreamReader) Event() model.StreamEvent { return s.cur }

func (s *responsesStreamReader) Err() error { return s.err }

func (s *responsesStreamReader) Close() error {
	if s.resp == nil {
		return nil
	}
	err := s.resp.Body.Close()
	s.resp = nil
	return err
}
