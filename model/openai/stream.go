package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/model/sse"
)

// streamRequest wraps the shared request shape with the streaming flags.
type streamRequest struct {
	model.Request
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatStream performs a streaming chat completion request and implements
// model.StreamModel.
//
// Event semantics:
//   - text and tool-call fragments are forwarded as they arrive, in wire
//     order;
//   - exactly one model.StreamFinish event (finish reason, usage, response
//     identity) is emitted last;
//   - a stream is clean if the server sent [DONE] or a finish_reason chunk;
//     a connection closed before both yields a non-retryable protocol error,
//     so truncated streams fail loudly instead of appearing complete.
//
// Request-level failures (non-2xx, transport) return an error and no reader;
// context.Canceled passes through unchanged.
func (c *Client) ChatStream(ctx context.Context, req *model.Request) (model.StreamReader, error) {
	if req == nil {
		return nil, &model.ModelError{Kind: model.ErrorInvalidRequest, Provider: provider, Err: errors.New("nil request")}
	}
	wire := streamRequest{Request: *req, Stream: true}
	if wire.Model == "" {
		wire.Model = c.defaultModel
	}
	if wire.Model == "" {
		return nil, &model.ModelError{
			Kind:     model.ErrorInvalidRequest,
			Provider: provider,
			Err:      errors.New("no model specified in request or Config.DefaultModel"),
		}
	}
	if !c.disableStreamUsage {
		wire.StreamOptions = &streamOptions{IncludeUsage: true}
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
	if resp.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			return nil, model.ClassifyTransportError(provider, readErr)
		}
		return nil, model.NewHTTPError(provider, resp.StatusCode, string(data))
	}
	return &streamReader{provider: provider, resp: resp, dec: sse.NewDecoder(resp.Body)}, nil
}

type streamReader struct {
	provider string
	resp     *http.Response
	dec      *sse.Decoder

	queue []model.StreamEvent
	cur   model.StreamEvent
	err   error

	finishSeen   bool
	finishReason string
	doneSeen     bool
	usage        model.Usage
	id, mdl      string
	finished     bool
}

func (s *streamReader) Next() bool {
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
			if !s.finishSeen && !s.doneSeen {
				s.err = &model.ModelError{
					Kind:     model.ErrorProtocol,
					Provider: s.provider,
					Err:      errors.New("stream ended before finish_reason or [DONE]"),
				}
				return false
			}
			s.emitFinish()
		case err != nil:
			s.err = model.ClassifyTransportError(s.provider, err)
			return false
		default:
			data := strings.TrimSpace(ev.Data)
			if data == "" {
				continue
			}
			if data == doneSentinel {
				s.doneSeen = true
				s.emitFinish()
				continue
			}
			if !s.handleChunk(data) {
				return false
			}
		}
	}
}

// doneSentinel terminates an OpenAI chat completion stream.
const doneSentinel = "[DONE]"

func (s *streamReader) handleChunk(data string) bool {
	var chunk chatChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		s.err = &model.ModelError{
			Kind:     model.ErrorProtocol,
			Provider: s.provider,
			Body:     truncate([]byte(data), 512),
			Err:      fmt.Errorf("decode chunk: %w", err),
		}
		return false
	}
	if chunk.ID != "" {
		s.id = chunk.ID
	}
	if chunk.Model != "" {
		s.mdl = chunk.Model
	}
	if chunk.Usage != nil {
		s.usage = *chunk.Usage
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason != "" {
			s.finishSeen = true
			s.finishReason = ch.FinishReason
		}
		if ch.Delta.Content != "" {
			s.queue = append(s.queue, model.StreamEvent{Type: model.StreamTextDelta, Text: ch.Delta.Content})
		}
		for _, tc := range ch.Delta.ToolCalls {
			s.queue = append(s.queue, model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: tc.toToolCall()})
		}
	}
	return true
}

func (s *streamReader) emitFinish() {
	s.finished = true
	s.queue = append(s.queue, model.StreamEvent{
		Type:         model.StreamFinish,
		FinishReason: s.finishReason,
		Usage:        s.usage,
		ID:           s.id,
		Model:        s.mdl,
	})
}

func (s *streamReader) Event() model.StreamEvent { return s.cur }

func (s *streamReader) Err() error { return s.err }

// Close releases the underlying response body. It is safe to call more than
// once and after the stream has been fully drained.
func (s *streamReader) Close() error {
	if s.resp == nil {
		return nil
	}
	err := s.resp.Body.Close()
	s.resp = nil
	return err
}

// chatChunk is one chat.completion.chunk SSE payload.
type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int       `json:"index"`
		Delta        wireDelta `json:"delta"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage *model.Usage `json:"usage"`
}

type wireDelta struct {
	Role      string              `json:"role"`
	Content   string              `json:"content"`
	ToolCalls []wireToolCallDelta `json:"tool_calls"`
}

type wireToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (t wireToolCallDelta) toToolCall() model.ToolCall {
	typ := t.Type
	if typ == "" {
		typ = "function"
	}
	return model.ToolCall{
		ID:       t.ID,
		Type:     typ,
		Index:    t.Index,
		Function: model.FunctionCall{Name: t.Function.Name, Arguments: t.Function.Arguments},
	}
}
