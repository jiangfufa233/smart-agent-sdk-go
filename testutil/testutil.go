// Package testutil provides scripted fake models and helpers for testing
// agents without network access. It is the same machinery the SDK's own
// tests and the offline example rely on.
package testutil

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// ErrScriptExhausted is returned when Chat is called more times than the
// script has steps — a useful failure signal for tests.
var ErrScriptExhausted = errors.New("testutil: script exhausted")

// Step is one scripted model response.
type Step struct {
	// Resp is returned as-is when Err and Func are zero.
	Resp *model.Response
	// Err is returned as the model error.
	Err error
	// Delay is slept (ctx-aware) before returning.
	Delay time.Duration
	// Func overrides Resp/Err and computes the response from the request,
	// which makes assertions on what the model received possible.
	Func func(req *model.Request) (*model.Response, error)
}

// TextStep returns a final-answer step.
func TextStep(text string) Step {
	return Step{Resp: &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: text},
		FinishReason: "stop",
	}}
}

// ToolCallStep returns a step requesting a single tool call.
func ToolCallStep(id, name, argsJSON string) Step {
	return Step{Resp: &model.Response{
		Message: model.Message{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:       id,
				Type:     "function",
				Function: model.FunctionCall{Name: name, Arguments: argsJSON},
			}},
		},
		FinishReason: "tool_calls",
	}}
}

// HTTPErrorStep returns a step failing with a classified HTTP error.
func HTTPErrorStep(status int, body string) Step {
	return Step{Err: model.NewHTTPError("scripted", status, body)}
}

// Scripted is a fake model that replays steps in order and records every
// request it receives. It is safe for concurrent use.
type Scripted struct {
	mu       sync.Mutex
	steps    []Step
	i        int
	requests []*model.Request
}

// NewScripted returns a model replaying the given steps in order.
func NewScripted(steps ...Step) *Scripted {
	return &Scripted{steps: steps}
}

// Chat implements model.Model.
func (s *Scripted) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	s.mu.Lock()
	cp := *req
	cp.Messages = append([]model.Message(nil), req.Messages...)
	cp.Tools = append([]model.ToolParam(nil), req.Tools...)
	s.requests = append(s.requests, &cp)
	if s.i >= len(s.steps) {
		s.mu.Unlock()
		return nil, ErrScriptExhausted
	}
	step := s.steps[s.i]
	s.i++
	s.mu.Unlock()

	if step.Delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(step.Delay):
		}
	}
	if step.Func != nil {
		return step.Func(req)
	}
	return step.Resp, step.Err
}

// Calls reports how many requests have been received.
func (s *Scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// LastRequest returns the most recent request, or nil.
func (s *Scripted) LastRequest() *model.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.requests); n > 0 {
		return s.requests[n-1]
	}
	return nil
}

// Requests returns a snapshot of all recorded requests.
func (s *Scripted) Requests() []*model.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*model.Request(nil), s.requests...)
}
