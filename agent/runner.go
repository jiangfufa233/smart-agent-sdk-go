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

	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/tool"
	"github.com/example/agent-sdk/tracing"
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
}

// Run starts a fresh conversation with a and sends it input.
func (r *Runner) Run(ctx context.Context, a *Agent, input string) (*RunResult, error) {
	var history []model.Message
	if a.Instructions != "" {
		history = append(history, model.Message{Role: model.RoleSystem, Content: a.Instructions})
	}
	return r.run(ctx, a, history, input)
}

// RunWithHistory continues an existing conversation (e.g. the Messages slice
// from a previous RunResult) with a new user input.
func (r *Runner) RunWithHistory(ctx context.Context, a *Agent, history []model.Message, input string) (*RunResult, error) {
	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	if a.Instructions != "" && (len(msgs) == 0 || msgs[0].Role != model.RoleSystem) {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: a.Instructions}}, msgs...)
	}
	return r.run(ctx, a, msgs, input)
}

type runOpts struct {
	runID       string
	maxTurns    int
	toolTimeout time.Duration
	maxOutput   int
	tracer      tracing.Tracer
}

func (r *Runner) run(ctx context.Context, a *Agent, history []model.Message, input string) (*RunResult, error) {
	if a.Model == nil {
		return nil, errors.New("agent: agent has no model configured")
	}
	opts := r.runOpts()

	hooks := r.hooks()
	spanCtx, span := opts.tracer.Start(ctx, "agent.run")
	span.Set("agent", a.Name)
	ctx = hooks.OnRunStart(spanCtx, a, opts.runID, input)

	started := time.Now()
	res, err := r.loop(ctx, a, history, input, opts)
	if res != nil {
		res.Duration = time.Since(started)
		res.RunID = opts.runID
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

func (r *Runner) loop(ctx context.Context, a *Agent, history []model.Message, input string, opts runOpts) (*RunResult, error) {
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
		resp, err := a.Model.Chat(callCtx, req)
		hooks.OnLLMResponse(callCtx, a, opts.runID, turn, resp, err, time.Since(t0))
		callSpan.End(err)
		if err != nil {
			return nil, fmt.Errorf("agent: model call failed: %w", err)
		}
		usage.Accumulate(resp.Usage)

		msg := resp.Message
		if len(msg.ToolCalls) == 0 {
			msgs = append(msgs, msg)
			return &RunResult{
				Output:       msg.Content,
				FinalMessage: msg,
				Messages:     msgs,
				ToolErrors:   toolErrs,
				Usage:        usage,
			}, nil
		}

		// Assistant message requesting tool calls, then one result per call.
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
