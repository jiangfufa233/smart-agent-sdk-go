package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
	"github.com/jiangfufa233/smart-agent-sdk-go/tracing"
)

const (
	defaultMaxTurns = 10
	// Tool output is fed back into model context, so an unbounded result can
	// blow the context window or the token budget. Cap it unless configured.
	defaultMaxToolOutput = 512 << 10
)

// Runner executes the agent loop: call the model, execute any requested
// tool calls, feed results back, repeat until a final answer is produced.
//
// A Runner is stateless per run and safe for concurrent use.
type Runner struct {
	// MaxTurns bounds the number of model calls per run (default 10).
	MaxTurns int
	// Hooks receives lifecycle events; nil means no-op.
	Hooks Hooks
	// Tracer creates spans for the run, each model call and each tool call;
	// nil means no-op.
	Tracer tracing.Tracer
	// ToolTimeout bounds each tool execution; zero means no limit.
	ToolTimeout time.Duration
	// MaxToolOutputBytes truncates tool results fed back to the model
	// (default 512 KiB).
	MaxToolOutputBytes int
	// Compressor shrinks the loaded history before it is sent to the model
	// (view-level only; sessions still store the full transcript); nil
	// means no compression.
	Compressor HistoryCompressor
}

// NewRunner returns a Runner with default settings.
func NewRunner() *Runner {
	return &Runner{MaxTurns: defaultMaxTurns}
}

// RunResult is the outcome of a run.
type RunResult struct {
	// Output is the agent's final textual answer.
	Output string
	// FinalMessage is the model message that ended the run.
	FinalMessage model.Message
	// Messages is the full conversation including system prompt, user input,
	// tool calls and tool results. It can be passed back to RunWithHistory
	// for multi-turn conversations.
	Messages []model.Message
	// ToolErrors lists tool invocations that failed during the run. Their
	// error text was still fed back to the model.
	ToolErrors []*ToolError
	// Usage is the total token consumption across all model calls of the
	// run.
	Usage model.Usage
	// Duration is the wall time of the whole run.
	Duration time.Duration
	// RunID identifies this run in hooks and traces.
	RunID string
	// Agent is the agent that produced the final output. It differs from the
	// agent the run started with when a handoff occurred.
	Agent *Agent
	// Transfers lists, in order, the names of the agents the conversation
	// was handed to during the run.
	Transfers []string
}

// Run starts a fresh conversation with a and sends it input.
func (r *Runner) Run(ctx context.Context, a *Agent, input string) (*RunResult, error) {
	return r.run(ctx, a, withInstructions(a, nil), input, nil)
}

// RunWithHistory continues an existing conversation (e.g. the Messages slice
// from a previous RunResult) with a new user input.
func (r *Runner) RunWithHistory(ctx context.Context, a *Agent, history []model.Message, input string) (*RunResult, error) {
	return r.run(ctx, a, withInstructions(a, history), input, nil)
}

type runOpts struct {
	runID       string
	maxTurns    int
	toolTimeout time.Duration
	maxOutput   int
	tracer      tracing.Tracer
}

func (r *Runner) run(ctx context.Context, a *Agent, history []model.Message, input string, sess Session) (*RunResult, error) {
	if a.Model == nil {
		return nil, errors.New("agent: agent has no model configured")
	}
	opts := r.runOpts()

	hooks := r.hooks()
	spanCtx, span := opts.tracer.Start(ctx, "agent.run")
	span.Set("agent", a.Name)
	ctx = hooks.OnRunStart(spanCtx, a, opts.runID, input)

	history, cerr := r.compressHistory(ctx, opts, history)
	var res *RunResult
	var err error
	if cerr != nil {
		err = cerr
	} else {
		started := time.Now()
		res, err = r.loop(ctx, a, history, input, opts, sess)
		if res != nil {
			res.Duration = time.Since(started)
			res.RunID = opts.runID
		}
	}
	hooks.OnRunEnd(ctx, a, opts.runID, res, err)
	span.End(err)
	return res, err
}

// runOpts resolves defaults and generates the run ID.
func (r *Runner) runOpts() runOpts {
	opts := runOpts{
		maxTurns:    r.MaxTurns,
		toolTimeout: r.ToolTimeout,
		maxOutput:   r.MaxToolOutputBytes,
		tracer:      r.Tracer,
	}
	if opts.maxTurns <= 0 {
		opts.maxTurns = defaultMaxTurns
	}
	if opts.maxOutput <= 0 {
		opts.maxOutput = defaultMaxToolOutput
	}
	if opts.tracer == nil {
		opts.tracer = tracing.Nop()
	}
	opts.runID = newRunID()
	return opts
}

func (r *Runner) loop(ctx context.Context, a *Agent, history []model.Message, input string, opts runOpts, sess Session) (*RunResult, error) {
	hooks := r.hooks()

	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: input})

	// cur is the agent currently handling the conversation; a handoff tool
	// call swaps it (tool surface, settings and system prompt) mid-run.
	cur := a
	regs, err := buildAgentRegs(cur)
	if err != nil {
		return nil, err
	}
	settings := settingsFor(cur)

	// Input guardrails gate the run before any model call.
	if err := runInputGuardrails(ctx, a, input, hooks, opts.tracer, opts.runID); err != nil {
		return nil, err
	}

	var toolErrs []*ToolError
	var usage model.Usage
	var transfers []string

	for turn := 0; turn < opts.maxTurns; turn++ {
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
		resp, err := cur.Model.Chat(callCtx, req)
		hooks.OnLLMResponse(callCtx, cur, opts.runID, turn, resp, err, time.Since(t0))
		callSpan.End(err)
		if err != nil {
			return nil, fmt.Errorf("agent: model call failed: %w", err)
		}
		usage.Accumulate(resp.Usage)

		msg := resp.Message
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
			// Output guardrails gate the result before it is published; the
			// final agent's guardrails apply. On success the new messages
			// (user input plus everything generated) are persisted to the
			// session; a writeback failure fails the run.
			if err := runOutputGuardrails(ctx, cur, res, hooks, opts.tracer, opts.runID); err != nil {
				return nil, err
			}
			if err := persistTurn(ctx, sess, res, len(history)); err != nil {
				return nil, err
			}
			return res, nil
		}

		// Assistant message requesting tool calls, then one result per call.
		// Every call must get a result to keep the transcript valid, so a
		// handoff only takes effect after all of them are recorded.
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
			result, terr := r.execTool(toolCtx, tc.Function.Name, tc.Function.Arguments, regs.tools, opts)
			var errOut error
			if terr != nil {
				errOut = terr
				toolErrs = append(toolErrs, terr)
			}
			hooks.OnToolResult(toolCtx, cur, opts.runID, tc.Function.Name, result, errOut, time.Since(t1))
			toolSpan.End(errOut)
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
				return nil, fmt.Errorf("agent: switch to agent %q: %w", next.name(), err)
			}
			settings = settingsFor(next)
			msgs = switchSystemMessage(msgs, next)
			cur = next
		}
	}
	return nil, &MaxTurnsError{MaxTurns: opts.maxTurns}
}

// execTool invokes one tool and always returns the text to feed back to the
// model, converting failures (including panics) into a *ToolError.
func (r *Runner) execTool(ctx context.Context, name, args string, byName map[string]tool.Tool, opts runOpts) (result string, terr *ToolError) {
	defer func() {
		if p := recover(); p != nil {
			terr = &ToolError{Tool: name, Arguments: args, Err: fmt.Errorf("panic: %v", p)}
			result = "error: tool " + strconv.Quote(name) + " panicked: " + fmt.Sprint(p)
		}
	}()

	t, ok := byName[name]
	if !ok {
		terr = &ToolError{Tool: name, Arguments: args, Err: errors.New("unknown tool")}
		return "error: unknown tool " + strconv.Quote(name), terr
	}

	if opts.toolTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.toolTimeout)
		defer cancel()
	}

	out, err := t.Run(ctx, args)
	if err != nil {
		terr = &ToolError{Tool: name, Arguments: args, Err: err}
		return fmt.Sprintf("error: tool %q failed: %v", name, err), terr
	}
	return truncateOutput(out, opts.maxOutput), nil
}

// hooks never returns a nil interface value.
func (r *Runner) hooks() Hooks {
	if r.Hooks == nil {
		return NopHooks
	}
	return r.Hooks
}

func truncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n[output truncated, dropped %d bytes]", len(s)-len(cut))
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
