package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// StreamEventType discriminates the kind of a StreamEvent.
type StreamEventType string

const (
	StreamRunStarted       StreamEventType = "run_started"
	StreamTaskStarted      StreamEventType = "task_started"
	StreamTextDelta        StreamEventType = "text_delta"
	StreamReasoningDelta   StreamEventType = "reasoning_delta"
	StreamToolCallStarted  StreamEventType = "tool_call_started"
	StreamToolCallArgs     StreamEventType = "tool_call_args"
	StreamToolCallFinished StreamEventType = "tool_call_finished"
	StreamToolResult       StreamEventType = "tool_result"
	StreamHandoff          StreamEventType = "handoff"
	StreamTaskCompleted    StreamEventType = "task_completed"
	StreamTaskFailed       StreamEventType = "task_failed"
	StreamTaskSkipped      StreamEventType = "task_skipped"
	StreamFinalOutput      StreamEventType = "final_output"
	StreamRunError         StreamEventType = "run_error"
)

// AgentRef identifies an agent in a stream event. ID is a stable machine
// identifier and Name is a human-readable display name.
type AgentRef struct {
	ID   string
	Name string
}

// TaskRef correlates an event with a task and its root stream. Standalone runs
// use the root run ID as both ID and SourceRunID; delegated and workflow tasks
// replace ID and ParentID while retaining the common RootRunID.
type TaskRef struct {
	ID          string
	ParentID    string
	RootRunID   string
	SourceRunID string
}

type taskContextKey struct{}

// TaskFromContext returns the task correlation for an active run. Hooks can
// use it to correlate logs across a delegated task tree.
func TaskFromContext(ctx context.Context) (TaskRef, bool) {
	task, ok := ctx.Value(taskContextKey{}).(TaskRef)
	return task, ok
}

func withTask(ctx context.Context, task TaskRef) context.Context {
	return context.WithValue(ctx, taskContextKey{}, task)
}

// WithTask returns a context that requests task correlation for a public
// Runner entry point. The runner assigns SourceRunID for the new agent run.
func WithTask(ctx context.Context, task TaskRef) context.Context {
	return withTask(ctx, task)
}

// StreamEvent is one incremental update from a run. Every event carries the
// active agent and task that produced it.
type StreamEvent struct {
	Type  StreamEventType
	Agent AgentRef
	Task  TaskRef
	Turn  int // zero-based model-call index the event belongs to
	// Text carries a StreamTextDelta fragment; on StreamFinalOutput it is the
	// complete visible output.
	Text string
	// Reasoning carries a StreamReasoningDelta fragment. It is observable only
	// and never becomes a model message, tool transcript, session item, or run
	// output.
	Reasoning string
	// Call is the tool call for the tool-call lifecycle events. On
	// StreamToolCallArgs, Call.Function.Arguments holds only the new raw
	// fragment. It is not guaranteed to be valid JSON until
	// StreamToolCallFinished.
	Call model.ToolCall
	// Result is the tool output fed back to the model (StreamToolResult).
	Result string
	// ToolErr is non-nil on StreamToolResult when the tool failed.
	ToolErr *ToolError
	// From and To identify both sides of a StreamHandoff boundary.
	From AgentRef
	To   AgentRef
	// FinishReason is reported on StreamFinalOutput.
	FinishReason string
	// Usage is the accumulated provider-reported run usage, reported on
	// StreamFinalOutput.
	Usage model.Usage
	// Err is the terminal failure (StreamRunError) or task failure detail.
	Err error
}

func agentRef(a *Agent) AgentRef {
	if a == nil {
		return AgentRef{}
	}
	return AgentRef{ID: a.ID, Name: a.Name}
}

func streamEvent(opts runOpts, a *Agent, typ StreamEventType, turn int) StreamEvent {
	return streamEventWithTask(opts.task, a, typ, turn)
}

func streamEventWithTask(task TaskRef, a *Agent, typ StreamEventType, turn int) StreamEvent {
	return StreamEvent{Type: typ, Agent: agentRef(a), Task: task, Turn: turn}
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

// Result blocks until the run ends and reports its outcome. It does not drain
// Events; consume Events or call Wait to avoid stalling the run.
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
	return r.runStreamAsync(ctx, a, withInstructions(a, nil), input, nil)
}

// RunStreamWithHistory continues an existing conversation while streaming,
// mirroring RunWithHistory.
func (r *Runner) RunStreamWithHistory(ctx context.Context, a *Agent, history []model.Message, input string) *StreamRun {
	return r.runStreamAsync(ctx, a, withInstructions(a, history), input, nil)
}

func (r *Runner) runStreamAsync(ctx context.Context, a *Agent, history []model.Message, input string, sess Session) *StreamRun {
	return r.runStreamAsyncWithOpts(ctx, a, history, input, sess, r.runOptsForContext(ctx))
}

func (r *Runner) runStreamAsyncWithOpts(ctx context.Context, a *Agent, history []model.Message, input string, sess Session, opts runOpts) *StreamRun {
	ch := make(chan StreamEvent, streamEventBuffer)
	done := make(chan struct{})
	sr := &StreamRun{Events: ch, done: done}
	go func() {
		defer close(done)
		defer close(ch)
		res, err := r.runStream(ctx, a, history, input, ch, sess, opts)
		sr.res, sr.err = res, err
	}()
	return sr
}

func (r *Runner) runStream(ctx context.Context, a *Agent, history []model.Message, input string, ch chan<- StreamEvent, sess Session, opts runOpts) (*RunResult, error) {
	if err := validateAgent(a); err != nil {
		ev := streamEvent(opts, a, StreamRunError, 0)
		ev.Err = err
		ch <- ev
		return nil, err
	}

	hooks := r.hooks()
	spanCtx, span := opts.tracer.Start(ctx, "agent.run")
	span.Set("agent", a.Name)
	ctx = hooks.OnRunStart(withTask(spanCtx, opts.task), a, opts.runID, input)
	ctx = withTask(ctx, opts.task)

	sendEvent(ctx, ch, streamEvent(opts, a, StreamRunStarted, 0))

	history, cerr := r.compressHistory(ctx, opts, history)
	var res *RunResult
	var active *Agent
	var err error
	if cerr != nil {
		active = a
		err = cerr
	} else {
		started := time.Now()
		res, active, err = r.streamLoop(ctx, a, history, input, opts, ch, sess)
		if res != nil {
			res.Duration = time.Since(started)
			res.RunID = opts.runID
		}
	}
	hooks.OnRunEnd(ctx, a, opts.runID, res, err)
	span.End(err)
	if err != nil {
		if active == nil {
			active = a
		}
		ev := streamEvent(opts, active, StreamRunError, 0)
		ev.Err = err
		ch <- ev
	}
	return res, err
}

func (r *Runner) streamLoop(ctx context.Context, a *Agent, history []model.Message, input string, opts runOpts, ch chan<- StreamEvent, sess Session) (*RunResult, *Agent, error) {
	hooks := r.hooks()

	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: input})

	cur := a
	regs, err := buildAgentRegs(cur)
	if err != nil {
		return nil, cur, err
	}
	settings := settingsFor(cur)

	if err := runInputGuardrails(ctx, a, input, hooks, opts.tracer, opts.runID); err != nil {
		return nil, cur, err
	}

	var toolErrs []*ToolError
	var usage model.Usage
	var transfers []string

	for turn := 0; turn < opts.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return nil, cur, err
		}
		req := &model.Request{
			Model:    cur.ModelName,
			Messages: msgs,
			Tools:    regs.specs,
			Settings: settings,
		}
		hooks.OnLLMCall(ctx, cur, opts.runID, turn, req)
		callCtx, callSpan := opts.tracer.Start(ctx, "model.chat")
		callSpan.Set("model", cur.ModelName)
		t0 := time.Now()
		msg, turnUsage, finishReason, err := r.streamTurn(callCtx, cur, req, turn, opts, ch)
		var resp *model.Response
		if err == nil {
			resp = &model.Response{Message: msg, FinishReason: finishReason, Usage: turnUsage}
		}
		hooks.OnLLMResponse(ctx, cur, opts.runID, turn, resp, err, time.Since(t0))
		callSpan.End(err)
		if err != nil {
			return nil, cur, fmt.Errorf("agent: model call failed: %w", err)
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
				Agent:        cur,
				Transfers:    transfers,
			}
			if err := runOutputGuardrails(ctx, cur, res, hooks, opts.tracer, opts.runID); err != nil {
				return nil, cur, err
			}
			if err := persistTurn(ctx, sess, res, len(history)); err != nil {
				return nil, cur, err
			}
			ev := streamEvent(opts, cur, StreamFinalOutput, turn)
			ev.Text = msg.Content
			ev.FinishReason = finishReason
			ev.Usage = usage
			ch <- ev
			return res, cur, nil
		}

		msgs = append(msgs, msg)
		var next *Agent
		for _, tc := range msg.ToolCalls {
			if h, ok := regs.handoffs[tc.Function.Name]; ok {
				toolCtx, toolSpan := opts.tracer.Start(ctx, "tool."+tc.Function.Name)
				hooks.OnToolCall(toolCtx, cur, opts.runID, tc.Function.Name, tc.Function.Arguments)
				result := handoffToolOutput(h)
				hooks.OnToolResult(toolCtx, cur, opts.runID, tc.Function.Name, result, nil, 0)
				toolSpan.End(nil)
				if hh, ok := hooks.(HandoffHook); ok {
					hh.OnHandoff(ctx, cur, h.Target, opts.runID)
				}
				ev := streamEvent(opts, cur, StreamToolResult, turn)
				ev.Call = tc
				ev.Result = result
				if !sendEvent(ctx, ch, ev) {
					return nil, cur, ctx.Err()
				}
				ev = streamEvent(opts, cur, StreamHandoff, turn)
				ev.From = agentRef(cur)
				ev.To = agentRef(h.Target)
				if !sendEvent(ctx, ch, ev) {
					return nil, cur, ctx.Err()
				}
				msgs = append(msgs, model.Message{
					Role:       model.RoleTool,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    result,
				})
				transfers = append(transfers, h.Target.name())
				next = h.Target
				continue
			}
			toolCtx, toolSpan := opts.tracer.Start(ctx, "tool."+tc.Function.Name)
			hooks.OnToolCall(toolCtx, cur, opts.runID, tc.Function.Name, tc.Function.Arguments)
			t1 := time.Now()
			result, terr := r.execStreamTool(toolCtx, tc.Function.Name, tc.Function.Arguments, regs.tools, turn, opts, ch)
			var errOut error
			if terr != nil {
				errOut = terr
				toolErrs = append(toolErrs, terr)
			}
			hooks.OnToolResult(toolCtx, cur, opts.runID, tc.Function.Name, result, errOut, time.Since(t1))
			toolSpan.End(errOut)
			ev := streamEvent(opts, cur, StreamToolResult, turn)
			ev.Call = tc
			ev.Result = result
			ev.ToolErr = terr
			if !sendEvent(ctx, ch, ev) {
				return nil, cur, ctx.Err()
			}
			msgs = append(msgs, model.Message{
				Role:       model.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
		if next != nil {
			regs, err = buildAgentRegs(next)
			if err != nil {
				return nil, cur, fmt.Errorf("agent: switch to agent %q: %w", next.name(), err)
			}
			settings = settingsFor(next)
			msgs = switchSystemMessage(msgs, next)
			cur = next
		}
	}
	return nil, cur, &MaxTurnsError{MaxTurns: opts.maxTurns}
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
			ev := streamEvent(opts, a, StreamTextDelta, turn)
			ev.Text = resp.Message.Content
			if !sendEvent(ctx, ch, ev) {
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
			out := streamEvent(opts, a, StreamTextDelta, turn)
			out.Text = ev.Text
			if !sendEvent(ctx, ch, out) {
				return msg, usage, "", ctx.Err()
			}
		case model.StreamReasoningDelta:
			out := streamEvent(opts, a, StreamReasoningDelta, turn)
			out.Reasoning = ev.Reasoning
			if !sendEvent(ctx, ch, out) {
				return msg, usage, "", ctx.Err()
			}
		case model.StreamToolCallDelta:
			calls.Add(ev.ToolCall)
			if !started[ev.ToolCall.Index] {
				started[ev.ToolCall.Index] = true
				out := streamEvent(opts, a, StreamToolCallStarted, turn)
				out.Call = ev.ToolCall
				if !sendEvent(ctx, ch, out) {
					return msg, usage, "", ctx.Err()
				}
			}
			if ev.ToolCall.Function.Arguments != "" {
				out := streamEvent(opts, a, StreamToolCallArgs, turn)
				out.Call = ev.ToolCall
				if !sendEvent(ctx, ch, out) {
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
		out := streamEvent(opts, a, StreamToolCallFinished, turn)
		out.Call = tc
		if !sendEvent(ctx, ch, out) {
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
