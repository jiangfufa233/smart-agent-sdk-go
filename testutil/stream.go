package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/example/agent-sdk/model"
)

// StreamStep scripts one streaming model call.
type StreamStep struct {
	// Deltas are emitted in order before the terminal events.
	Deltas []model.StreamEvent
	// FinishReason and Usage are reported on the terminal StreamFinish
	// event (and materialized by Chat).
	FinishReason string
	Usage        model.Usage
	// Err fails the request: ChatStream returns it and no reader exists.
	Err error
	// StreamErr aborts the stream after all Deltas were delivered, as if
	// the connection broke.
	StreamErr error
	// DeltaDelay is slept (ctx-aware) between deltas, to exercise
	// cancellation and slow consumers.
	DeltaDelay time.Duration
}

// TextChunk returns a text-delta stream event.
func TextChunk(s string) model.StreamEvent {
	return model.StreamEvent{Type: model.StreamTextDelta, Text: s}
}

// ToolCallChunk returns a tool-call-delta stream event.
func ToolCallChunk(tc model.ToolCall) model.StreamEvent {
	return model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: tc}
}

// ScriptedStream is a fake model replaying StreamSteps in order. It
// implements both model.Model and model.StreamModel — Chat materializes the
// step as a full response — and records every request. Safe for concurrent
// use.
type ScriptedStream struct {
	mu       sync.Mutex
	steps    []StreamStep
	i        int
	requests []*model.Request
}

// NewScriptedStream returns a streaming model replaying the given steps.
func NewScriptedStream(steps ...StreamStep) *ScriptedStream {
	return &ScriptedStream{steps: steps}
}

func (s *ScriptedStream) record(req *model.Request) (StreamStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *req
	cp.Messages = append([]model.Message(nil), req.Messages...)
	cp.Tools = append([]model.ToolParam(nil), req.Tools...)
	s.requests = append(s.requests, &cp)
	if s.i >= len(s.steps) {
		return StreamStep{}, false
	}
	step := s.steps[s.i]
	s.i++
	return step, true
}

// materialize builds the full response of a step, as a non-streaming
// provider would return it.
func materialize(step StreamStep) *model.Response {
	var acc model.ToolCallAccumulator
	var text string
	for _, d := range step.Deltas {
		switch d.Type {
		case model.StreamTextDelta:
			text += d.Text
		case model.StreamToolCallDelta:
			acc.Add(d.ToolCall)
		}
	}
	return &model.Response{
		Message: model.Message{
			Role:      model.RoleAssistant,
			Content:   text,
			ToolCalls: acc.Calls(),
		},
		FinishReason: step.FinishReason,
		Usage:        step.Usage,
	}
}

// Chat implements model.Model.
func (s *ScriptedStream) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	step, ok := s.record(req)
	if !ok {
		return nil, ErrScriptExhausted
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return materialize(step), nil
}

// ChatStream implements model.StreamModel.
func (s *ScriptedStream) ChatStream(ctx context.Context, req *model.Request) (model.StreamReader, error) {
	step, ok := s.record(req)
	if !ok {
		return nil, ErrScriptExhausted
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return &scriptStreamReader{step: step, ctx: ctx}, nil
}

// Calls reports how many requests have been received.
func (s *ScriptedStream) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// LastRequest returns the most recent request, or nil.
func (s *ScriptedStream) LastRequest() *model.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.requests); n > 0 {
		return s.requests[n-1]
	}
	return nil
}

type scriptStreamReader struct {
	step     StreamStep
	ctx      context.Context
	i        int
	cur      model.StreamEvent
	err      error
	closed   bool
	finished bool
}

func (r *scriptStreamReader) Next() bool {
	if r.err != nil || r.closed || r.finished {
		return false
	}
	if r.i < len(r.step.Deltas) {
		if r.step.DeltaDelay > 0 {
			select {
			case <-r.ctx.Done():
				r.err = r.ctx.Err()
				return false
			case <-time.After(r.step.DeltaDelay):
			}
		}
		r.cur = r.step.Deltas[r.i]
		r.i++
		return true
	}
	if r.step.StreamErr != nil {
		r.err = r.step.StreamErr
		return false
	}
	r.finished = true
	r.cur = model.StreamEvent{
		Type:         model.StreamFinish,
		FinishReason: r.step.FinishReason,
		Usage:        r.step.Usage,
	}
	return true
}

func (r *scriptStreamReader) Event() model.StreamEvent { return r.cur }

func (r *scriptStreamReader) Err() error { return r.err }

func (r *scriptStreamReader) Close() error {
	r.closed = true
	return nil
}
