package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// StreamEventType discriminates the kind of a StreamEvent.
type StreamEventType string

const (
	StreamRunStarted       StreamEventType = "run_started"
	StreamTextDelta        StreamEventType = "text_delta"
	StreamToolCallStarted  StreamEventType = "tool_call_started"
	StreamToolCallArgs     StreamEventType = "tool_call_args"
	StreamToolCallFinished StreamEventType = "tool_call_finished"
	StreamToolResult       StreamEventType = "tool_result"
	StreamFinalOutput      StreamEventType = "final_output"
	StreamRunError         StreamEventType = "run_error"
)

// StreamEvent is one incremental update from a run.
type StreamEvent struct {
	Type  StreamEventType
	RunID string
	Agent string
	Turn  int // zero-based model-call index the event belongs to
	// Text carries a StreamTextDelta fragment; on StreamFinalOutput it is
	// the complete output.
	Text string
	// Call is the tool call for the tool-call lifecycle events. On
	// StreamToolCallArgs, Call.Function.Arguments holds only the new
	// fragment.
	Call model.ToolCall
	// Result is the tool output fed back to the model (StreamToolResult).
	Result string
	// ToolErr is non-nil on StreamToolResult when the tool failed.
	ToolErr *ToolError
	// FinishReason is reported on StreamFinalOutput.
	FinishReason string
	// Usage is the accumulated run usage, reported on StreamFinalOutput.
	Usage model.Usage
	// Err is the terminal failure (StreamRunError).
	Err error
}

// streamEventBuffer bounds events produced ahead of the consumer. The run
// stalls when the buffer is full — the natural backpressure for slow
// consumers.
const streamEventBuffer = 256

// StreamRun is an in-progress run that produces events.
//
// Events always ends with StreamFinalOutput or StreamRunError followed by
// channel close. Keep consuming Events until it closes, or the run stalls
// once the buffer fills (cancel the context to abort a run you no longer
// read).
type StreamRun struct {
	Events <-chan StreamEvent

	done <-chan struct{}
	res  *RunResult
	err  error
}

// Result blocks until the run ends and reports its outcome. It does not
// drain Events; consume Events or call Wait to avoid stalling the run.
func (sr *StreamRun) Result() (*RunResult, error) {
	<-sr.done
	return sr.res, sr.err
}

// Wait drains the event stream to completion and returns the final result.
func (sr *StreamRun) Wait() (*RunResult, error) {
	for range sr.Events {
	}
	return sr.Result()
}

// RunStream executes the agent loop like Run, but emits incremental events
// instead of only the final result. If a.Model does not implement
// model.StreamModel, the run falls back to non-streaming model calls and
// reports each full answer as a single StreamTextDelta.
func (r *Runner) RunStream(ctx context.Context, a *Agent, input string) *StreamRun {
	var history []model.Message
	if a.Instructions != "" {
		history = append(history, model.Message{Role: model.RoleSystem, Content: a.Instructions})
	}
	return r.runStreamAsync(ctx, a, history, input)
}

// RunStreamWithHistory continues an existing conversation while streaming
// events, mirroring RunWithHistory.
func (r *Runner) RunStreamWithHistory(ctx context.Context, a *Agent, history []model.Message, input string) *StreamRun {
	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	if a.Instructions != "" && (len(msgs) == 0 || msgs[0].Role != model.RoleSystem) {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: a.Instructions}}, msgs...)
	}
	return r.runStreamAsync(ctx, a, msgs, input)
}

func (r *Runner) runStreamAsync(ctx context.Context, a *Agent, history []model.Message, input string) *StreamRun {
	ch := make(chan StreamEvent, streamEventBuffer)
	done := make(chan struct{})
	sr := &StreamRun{Events: ch, done: done}
	go func() {
		// LIFO: events channel closes before done, so Wait sees a closed
		// channel and then an already-published result.
		defer close(done)
		defer close(ch)
		res, err := r.runStream(ctx, a, history, input, ch)
		sr.res, sr.err = res, err
	}()
	return sr
}

func (r *Runner) runStream(ctx context.Context, a *Agent, history []model.Message, input string, ch chan<- StreamEvent) (*RunResult, error) {
	if a.Model == nil {
		err := errors.New("agent: agent has no model configured")
		ch <- StreamEvent{Type: StreamRunError, Err: err, Agent: a.Name}
		return nil, err
	}

	opts := r.runOpts()
	hooks := r.hooks()
	spanCtx, span := opts.tracer.Start(ctx, "agent.run")
	span.Set("agent", a.Name)
	ctx = hooks.OnRunStart(spanCtx, a, opts.runID, input)

	sendEvent(ctx, ch, StreamEvent{Type: StreamRunStarted, RunID: opts.runID, Agent: a.Name})

	started := time.Now()
	res, err := r.streamLoop(ctx, a, history, input, opts, ch)
	if res != nil {
		res.Duration = time.Since(started)
		res.RunID = opts.runID
	}
	hooks.OnRunEnd(ctx, a, opts.runID, res, err)
	span.End(err)
	if err != nil {
		// Terminal event: sent unconditionally so consumers always see an
		// outcome; the run has already ended.
		ch <- StreamEvent{Type: StreamRunError, Err: err, RunID: opts.runID, Agent: a.Name}
	}
	return res, err
}

func (r *Runner) streamLoop(ctx context.Context, a *Agent, history []model.Message, input string, opts runOpts, ch chan<- StreamEvent) (*RunResult, error) {
	hooks := r.hooks()

	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: input})

	specs := make([]model.ToolParam, 0, len(a.Tools))
	byName := make(map[string]tool.Tool, len(a.Tools))
	for _, t := range a.Tools {
		spec := t.Spec()
		specs = append(specs, spec)
		byName[spec.Function.Name] = t
	}

	var settings model.Settings
	if a.Settings != nil {
		settings = *a.Settings
	}

	var toolErrs []*ToolError
	var usage model.Usage

	for turn := 0; turn < opts.maxTurns; turn++ {
		req := &model.Request{
			Model:    a.ModelName,
			Messages: msgs,
			Tools:    specs,
			Settings: settings,
		}
		hooks.OnLLMCall(ctx, a, opts.runID, turn, req)
		callCtx, callSpan := opts.tracer.Start(ctx, "model.chat")
		callSpan.Set("model", a.ModelName)
		t0 := time.Now()
		msg, turnUsage, finishReason, err := r.streamTurn(callCtx, a, req, turn, opts, ch)
		var resp *model.Response
		if err == nil {
			resp = &model.Response{Message: msg, FinishReason: finishReason, Usage: turnUsage}
		}
		hooks.OnLLMResponse(ctx, a, opts.runID, turn, resp, err, time.Since(t0))
		callSpan.End(err)
		if err != nil {
			return nil, fmt.Errorf("agent: model call failed: %w", err)
		}
		usage.Accumulate(turnUsage)

		if len(msg.ToolCalls) == 0 {
			msgs = append(msgs, msg)
			res := &RunResult{
				Output:       msg.Content,
				FinalMessage: msg,
				Messages:     msgs,
				ToolErrors:   toolErrs,
				Usage:        usage,
			}
			ch <- StreamEvent{
				Type:         StreamFinalOutput,
				RunID:        opts.runID,
				Agent:        a.Name,
				Turn:         turn,
				Text:         msg.Content,
				FinishReason: finishReason,
				Usage:        usage,
			}
			return res, nil
		}

		msgs = append(msgs, msg)
		for _, tc := range msg.ToolCalls {
			toolCtx, toolSpan := opts.tracer.Start(ctx, "tool."+tc.Function.Name)
			hooks.OnToolCall(toolCtx, a, opts.runID, tc.Function.Name, tc.Function.Arguments)
			t1 := time.Now()
			result, terr := r.execTool(toolCtx, tc.Function.Name, tc.Function.Arguments, byName, opts)
			var errOut error
			if terr != nil {
				errOut = terr
				toolErrs = append(toolErrs, terr)
			}
			hooks.OnToolResult(toolCtx, a, opts.runID, tc.Function.Name, result, errOut, time.Since(t1))
			toolSpan.End(errOut)
			if !sendEvent(ctx, ch, StreamEvent{
				Type:    StreamToolResult,
				RunID:   opts.runID,
				Agent:   a.Name,
				Turn:    turn,
				Call:    tc,
				Result:  result,
				ToolErr: terr,
			}) {
				return nil, ctx.Err()
			}
			msgs = append(msgs, model.Message{
				Role:       model.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}
	return nil, &MaxTurnsError{MaxTurns: opts.maxTurns}
}

// streamTurn performs one model call, streaming when the model supports it
// and falling back to a plain call otherwise. It returns the assembled
// assistant message for the turn.
func (r *Runner) streamTurn(ctx context.Context, a *Agent, req *model.Request, turn int, opts runOpts, ch chan<- StreamEvent) (msg model.Message, usage model.Usage, finishReason string, err error) {
	sm, canStream := a.Model.(model.StreamModel)
	if !canStream {
		resp, cerr := a.Model.Chat(ctx, req)
		if cerr != nil {
			return msg, usage, "", cerr
		}
		if resp.Message.Content != "" {
			if !sendEvent(ctx, ch, StreamEvent{Type: StreamTextDelta, RunID: opts.runID, Agent: a.Name, Turn: turn, Text: resp.Message.Content}) {
				return msg, usage, "", ctx.Err()
			}
		}
		return resp.Message, resp.Usage, resp.FinishReason, nil
	}

	sr, cerr := sm.ChatStream(ctx, req)
	if cerr != nil {
		return msg, usage, "", cerr
	}
	defer func() { _ = sr.Close() }()

	var text strings.Builder
	var calls model.ToolCallAccumulator
	started := make(map[int]bool)

	for sr.Next() {
		ev := sr.Event()
		switch ev.Type {
		case model.StreamTextDelta:
			text.WriteString(ev.Text)
			if !sendEvent(ctx, ch, StreamEvent{Type: StreamTextDelta, RunID: opts.runID, Agent: a.Name, Turn: turn, Text: ev.Text}) {
				return msg, usage, "", ctx.Err()
			}
		case model.StreamToolCallDelta:
			calls.Add(ev.ToolCall)
			if !started[ev.ToolCall.Index] {
				started[ev.ToolCall.Index] = true
				if !sendEvent(ctx, ch, StreamEvent{Type: StreamToolCallStarted, RunID: opts.runID, Agent: a.Name, Turn: turn, Call: ev.ToolCall}) {
					return msg, usage, "", ctx.Err()
				}
			}
			if ev.ToolCall.Function.Arguments != "" {
				if !sendEvent(ctx, ch, StreamEvent{Type: StreamToolCallArgs, RunID: opts.runID, Agent: a.Name, Turn: turn, Call: ev.ToolCall}) {
					return msg, usage, "", ctx.Err()
				}
			}
		case model.StreamFinish:
			finishReason = ev.FinishReason
			usage = ev.Usage
		}
	}
	if err := sr.Err(); err != nil {
		return msg, usage, "", err
	}

	for _, tc := range calls.Calls() {
		if !sendEvent(ctx, ch, StreamEvent{Type: StreamToolCallFinished, RunID: opts.runID, Agent: a.Name, Turn: turn, Call: tc}) {
			return msg, usage, "", ctx.Err()
		}
	}

	msg = model.Message{Role: model.RoleAssistant, Content: text.String(), ToolCalls: calls.Calls()}
	return msg, usage, finishReason, nil
}

// sendEvent delivers ev, aborting (false) when ctx is done.
func sendEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
