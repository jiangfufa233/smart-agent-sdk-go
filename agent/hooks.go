package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// Hooks receives lifecycle events for every run executed by a Runner.
// Implementations must be safe for concurrent use: a single Runner may
// execute runs concurrently.
type Hooks interface {
	// OnRunStart is called before the first model call. The returned context
	// is used for the rest of the run.
	OnRunStart(ctx context.Context, a *Agent, runID, input string) context.Context
	// OnRunEnd is called after the run finishes; err is non-nil on failure.
	OnRunEnd(ctx context.Context, a *Agent, runID string, res *RunResult, err error)
	// OnLLMCall and OnLLMResponse bracket each model call; turn is 0-based.
	OnLLMCall(ctx context.Context, a *Agent, runID string, turn int, req *model.Request)
	OnLLMResponse(ctx context.Context, a *Agent, runID string, turn int, resp *model.Response, err error, elapsed time.Duration)
	// OnToolCall and OnToolResult bracket each tool execution.
	OnToolCall(ctx context.Context, a *Agent, runID string, name, args string)
	OnToolResult(ctx context.Context, a *Agent, runID string, name, result string, err error, elapsed time.Duration)
}

// HandoffHook is an optional extension of Hooks for first-class handoffs.
// A Hooks implementation that also implements this interface receives handoff
// events; implementations that do not are unaffected.
type HandoffHook interface {
	Hooks
	// OnHandoff is called when the run transfers control from one agent to
	// another through a handoff tool call.
	OnHandoff(ctx context.Context, from, to *Agent, runID string)
}

// GuardrailHook is an optional extension of Hooks for guardrail evaluations.
// A Hooks implementation that also implements this interface receives a
// verdict for every guardrail that runs; implementations that do not are
// unaffected.
type GuardrailHook interface {
	Hooks
	// OnGuardrail is called after each guardrail evaluation; err is non-nil
	// when the guardrail itself failed.
	OnGuardrail(ctx context.Context, a *Agent, runID string, stage GuardrailStage, name string, res GuardrailResult, err error, elapsed time.Duration)
}

// MultiHooks fans every lifecycle event out to each Hooks value in order. It
// is the way to run several hooks — e.g. SlogHooks and an audit logger — on
// one Runner. Optional extension interfaces (HandoffHook, GuardrailHook) are
// probed per element, so combining plain and extended implementations works
// as expected.
func MultiHooks(hooks ...Hooks) Hooks { return multiHooks(hooks) }

type multiHooks []Hooks

func (m multiHooks) OnRunStart(ctx context.Context, a *Agent, runID, input string) context.Context {
	for _, h := range m {
		ctx = h.OnRunStart(ctx, a, runID, input)
	}
	return ctx
}

func (m multiHooks) OnRunEnd(ctx context.Context, a *Agent, runID string, res *RunResult, err error) {
	for _, h := range m {
		h.OnRunEnd(ctx, a, runID, res, err)
	}
}

func (m multiHooks) OnLLMCall(ctx context.Context, a *Agent, runID string, turn int, req *model.Request) {
	for _, h := range m {
		h.OnLLMCall(ctx, a, runID, turn, req)
	}
}

func (m multiHooks) OnLLMResponse(ctx context.Context, a *Agent, runID string, turn int, resp *model.Response, err error, elapsed time.Duration) {
	for _, h := range m {
		h.OnLLMResponse(ctx, a, runID, turn, resp, err, elapsed)
	}
}

func (m multiHooks) OnToolCall(ctx context.Context, a *Agent, runID string, name, args string) {
	for _, h := range m {
		h.OnToolCall(ctx, a, runID, name, args)
	}
}

func (m multiHooks) OnToolResult(ctx context.Context, a *Agent, runID string, name, result string, err error, elapsed time.Duration) {
	for _, h := range m {
		h.OnToolResult(ctx, a, runID, name, result, err, elapsed)
	}
}

func (m multiHooks) OnHandoff(ctx context.Context, from, to *Agent, runID string) {
	for _, h := range m {
		if hh, ok := h.(HandoffHook); ok {
			hh.OnHandoff(ctx, from, to, runID)
		}
	}
}

func (m multiHooks) OnGuardrail(ctx context.Context, a *Agent, runID string, stage GuardrailStage, name string, res GuardrailResult, err error, elapsed time.Duration) {
	for _, h := range m {
		if gh, ok := h.(GuardrailHook); ok {
			gh.OnGuardrail(ctx, a, runID, stage, name, res, err, elapsed)
		}
	}
}

// NopHooks is the default no-op implementation.
var NopHooks Hooks = nopHooks{}

type nopHooks struct{}

func (nopHooks) OnRunStart(ctx context.Context, _ *Agent, _, _ string) context.Context {
	return ctx
}
func (nopHooks) OnRunEnd(context.Context, *Agent, string, *RunResult, error) {}
func (nopHooks) OnLLMCall(context.Context, *Agent, string, int, *model.Request) {
}
func (nopHooks) OnLLMResponse(context.Context, *Agent, string, int, *model.Response, error, time.Duration) {
}
func (nopHooks) OnToolCall(context.Context, *Agent, string, string, string) {}
func (nopHooks) OnToolResult(context.Context, *Agent, string, string, string, error, time.Duration) {
}

// SlogHooks logs every lifecycle event to l. It records identifiers, sizes
// and timings only — never raw input, tool output or API keys — so it is safe
// to enable in production.
func SlogHooks(l *slog.Logger) Hooks { return slogHooks{l: l} }

type slogHooks struct{ l *slog.Logger }

func (s slogHooks) OnRunStart(ctx context.Context, a *Agent, runID, input string) context.Context {
	s.l.Info("agent run start", "agent", a.Name, "run_id", runID, "input_chars", len(input))
	return ctx
}

func (s slogHooks) OnRunEnd(ctx context.Context, a *Agent, runID string, res *RunResult, err error) {
	if err != nil {
		s.l.Error("agent run end", "agent", a.Name, "run_id", runID, "error", err)
		return
	}
	s.l.Info("agent run end",
		"agent", a.Name,
		"run_id", runID,
		"output_chars", len(res.Output),
		"tool_errors", len(res.ToolErrors),
		"elapsed_ms", res.Duration.Milliseconds(),
	)
}

func (s slogHooks) OnLLMCall(ctx context.Context, a *Agent, runID string, turn int, req *model.Request) {
	s.l.Debug("llm call", "agent", a.Name, "run_id", runID, "turn", turn, "model", req.Model, "messages", len(req.Messages))
}

func (s slogHooks) OnLLMResponse(ctx context.Context, a *Agent, runID string, turn int, resp *model.Response, err error, elapsed time.Duration) {
	if err != nil {
		s.l.Warn("llm response", "agent", a.Name, "run_id", runID, "turn", turn, "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	s.l.Debug("llm response",
		"agent", a.Name,
		"run_id", runID,
		"turn", turn,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"elapsed_ms", elapsed.Milliseconds(),
	)
}

func (s slogHooks) OnToolCall(ctx context.Context, a *Agent, runID string, name, args string) {
	s.l.Debug("tool call", "agent", a.Name, "run_id", runID, "tool", name, "args_chars", len(args))
}

func (s slogHooks) OnToolResult(ctx context.Context, a *Agent, runID string, name, result string, err error, elapsed time.Duration) {
	if err != nil {
		s.l.Warn("tool result", "agent", a.Name, "run_id", runID, "tool", name, "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	s.l.Debug("tool result", "agent", a.Name, "run_id", runID, "tool", name, "result_chars", len(result), "elapsed_ms", elapsed.Milliseconds())
}

func (s slogHooks) OnHandoff(ctx context.Context, from, to *Agent, runID string) {
	s.l.Info("agent handoff", "from", from.name(), "to", to.name(), "run_id", runID)
}

func (s slogHooks) OnGuardrail(ctx context.Context, a *Agent, runID string, stage GuardrailStage, name string, res GuardrailResult, err error, elapsed time.Duration) {
	if err != nil {
		s.l.Warn("guardrail failed", "agent", a.Name, "run_id", runID, "stage", string(stage), "guardrail", name, "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	if res.Tripwire {
		s.l.Warn("guardrail tripwire", "agent", a.Name, "run_id", runID, "stage", string(stage), "guardrail", name, "info", res.Info)
		return
	}
	s.l.Debug("guardrail pass", "agent", a.Name, "run_id", runID, "stage", string(stage), "guardrail", name, "elapsed_ms", elapsed.Milliseconds())
}
