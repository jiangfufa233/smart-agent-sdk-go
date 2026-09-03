package model

import (
	"context"
	"sort"
)

// StreamEventType discriminates the kind of a StreamEvent. Exactly one kind
// is carried per event.
type StreamEventType string

const (
	// StreamTextDelta carries an incremental text fragment in Text.
	StreamTextDelta StreamEventType = "text"
	// StreamToolCallDelta carries a partial tool call in ToolCall. Index is
	// always set; ID, Name and Arguments are provider-dependent fragments
	// (arguments accumulate across deltas for the same Index).
	StreamToolCallDelta StreamEventType = "tool_call"
	// StreamFinish is the last event of a healthy stream. FinishReason and
	// Usage are set; no events follow it.
	StreamFinish StreamEventType = "finish"
)

// StreamEvent is one incremental update from a streaming model call.
type StreamEvent struct {
	Type StreamEventType
	// Text is the fragment for StreamTextDelta.
	Text string
	// ToolCall is the delta for StreamToolCallDelta.
	ToolCall ToolCall
	// FinishReason is the provider finish reason for StreamFinish
	// (e.g. "stop", "tool_calls", "length"). Empty if the provider did not
	// report one.
	FinishReason string
	// Usage is reported on StreamFinish when the provider sent it.
	Usage Usage
	// ID and Model identify the completion; set on StreamFinish when known.
	ID    string
	Model string
}

// StreamReader iterates the events of one streaming response.
//
// Usage pattern:
//
//	sr, err := m.ChatStream(ctx, req)
//	if err != nil { ... } // request-level failure
//	defer sr.Close()
//	for sr.Next() {
//	    ev := sr.Event()
//	    ...
//	}
//	if err := sr.Err(); err != nil { ... } // stream-level failure
//
// Next returns false when the stream is exhausted or failed; Err then
// reports a *ModelError for failures (context.Canceled passes through
// unchanged) or nil for a clean end. Event is only valid between a
// successful Next and the following call. Close is idempotent and must be
// called unless the stream was fully drained.
type StreamReader interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Close() error
}

// StreamModel is optionally implemented by providers that support streaming.
// Implementations must also implement Model so callers can fall back to
// non-streaming calls.
type StreamModel interface {
	Model
	ChatStream(ctx context.Context, req *Request) (StreamReader, error)
}

// AsStream wraps a non-streaming Model as a StreamModel whose streams emit
// the full response as a single text delta followed by StreamFinish. It lets
// stream-aware callers work uniformly with providers that cannot stream.
func AsStream(m Model) StreamModel {
	return asStream{m}
}

type asStream struct{ Model }

func (a asStream) ChatStream(ctx context.Context, req *Request) (StreamReader, error) {
	resp, err := a.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return NewStreamFromResponse(resp), nil
}

// NewStreamFromResponse returns a StreamReader replaying a complete Response
// as one text delta (when the message has content) plus StreamFinish. It is
// the terminal-state helper behind AsStream and is handy in tests.
func NewStreamFromResponse(resp *Response) StreamReader {
	r := &sliceReader{}
	if resp.Message.Content != "" {
		r.events = append(r.events, StreamEvent{Type: StreamTextDelta, Text: resp.Message.Content})
	}
	r.events = append(r.events, StreamEvent{
		Type:         StreamFinish,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		ID:           resp.ID,
		Model:        resp.Model,
	})
	return r
}

// sliceReader is a StreamReader over a fixed event list.
type sliceReader struct {
	events []StreamEvent
	i      int
	err    error
}

func (r *sliceReader) Next() bool {
	if r.i < len(r.events) {
		r.i++
		return true
	}
	return false
}

func (r *sliceReader) Event() StreamEvent { return r.events[r.i-1] }

func (r *sliceReader) Err() error { return r.err }

func (r *sliceReader) Close() error { return nil }

// ToolCallAccumulator assembles streamed tool-call deltas into complete
// calls. Deltas are merged by Index: the first delta for an index
// establishes ID and Name, argument fragments are concatenated. Calls
// returns the calls in Index order; call it after StreamFinish.
type ToolCallAccumulator struct {
	byIndex map[int]*ToolCall
}

// Add merges one delta.
func (a *ToolCallAccumulator) Add(delta ToolCall) {
	if a.byIndex == nil {
		a.byIndex = make(map[int]*ToolCall)
	}
	existing, ok := a.byIndex[delta.Index]
	if !ok {
		cp := delta
		a.byIndex[delta.Index] = &cp
		return
	}
	if delta.ID != "" {
		existing.ID = delta.ID
	}
	if delta.Function.Name != "" {
		existing.Function.Name = delta.Function.Name
	}
	existing.Function.Arguments += delta.Function.Arguments
}

// Calls returns the accumulated calls ordered by Index.
func (a *ToolCallAccumulator) Calls() []ToolCall {
	if len(a.byIndex) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(a.byIndex))
	for i := range a.byIndex {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]ToolCall, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, *a.byIndex[i])
	}
	return out
}
