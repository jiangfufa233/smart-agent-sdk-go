// Package audit provides a hooks implementation that records the full
// content of every run event — inputs, outputs, tool arguments and results,
// guardrail verdicts, handoffs and token usage — as structured log records.
//
// It is the compliance counterpart of agent.SlogHooks, which deliberately
// logs identifiers and sizes only. Audit records contain raw user content:
// route them to a protected sink and restrict who can read it.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// Logger records full-content audit events through slog. Construct with
// NewSlog; the zero value is not usable. *Logger implements agent.Hooks plus
// the HandoffHook and GuardrailHook extensions, so it sees every event.
//
// Every event is logged at Info level with stable attribute names and a
// message identifying the event kind. Wire it up with:
//
//	runner.Hooks = audit.NewSlog(slog.New(slog.NewJSONHandler(w, nil)))
//
// or combine it with other hooks via agent.MultiHooks.
type Logger struct{ l *slog.Logger }

// NewSlog returns an audit logger emitting to l. Pair l with
// slog.NewJSONHandler for one JSON object per line; any handler works.
func NewSlog(l *slog.Logger) *Logger { return &Logger{l: l} }

func (lg *Logger) OnRunStart(ctx context.Context, a *agent.Agent, runID, input string) context.Context {
	lg.l.Info("run_started", "agent", a.Name, "run_id", runID, "input", input)
	return ctx
}

func (lg *Logger) OnRunEnd(ctx context.Context, a *agent.Agent, runID string, res *agent.RunResult, err error) {
	if err != nil {
		lg.l.Info("run_ended", "agent", a.Name, "run_id", runID, "error", err.Error())
		return
	}
	attrs := []any{
		"agent", a.Name,
		"run_id", runID,
		"output", res.Output,
		"transfers", res.Transfers,
		"usage", marshal(res.Usage),
		"duration_ms", res.Duration.Milliseconds(),
	}
	if res.Agent != nil {
		attrs = append(attrs, "final_agent", res.Agent.Name)
	}
	if len(res.ToolErrors) > 0 {
		msgs := make([]string, len(res.ToolErrors))
		for i, te := range res.ToolErrors {
			msgs[i] = te.Error()
		}
		attrs = append(attrs, "tool_errors", msgs)
	}
	lg.l.Info("run_ended", attrs...)
}

func (lg *Logger) OnLLMCall(ctx context.Context, a *agent.Agent, runID string, turn int, req *model.Request) {
	lg.l.Info("llm_call",
		"agent", a.Name,
		"run_id", runID,
		"turn", turn,
		"model", req.Model,
		"messages", marshal(req.Messages),
		"tools", len(req.Tools),
	)
}

func (lg *Logger) OnLLMResponse(ctx context.Context, a *agent.Agent, runID string, turn int, resp *model.Response, err error, elapsed time.Duration) {
	if err != nil {
		lg.l.Info("llm_response",
			"agent", a.Name,
			"run_id", runID,
			"turn", turn,
			"error", err.Error(),
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return
	}
	lg.l.Info("llm_response",
		"agent", a.Name,
		"run_id", runID,
		"turn", turn,
		"content", resp.Message.Content,
		"tool_calls", marshal(resp.Message.ToolCalls),
		"finish_reason", resp.FinishReason,
		"usage", marshal(resp.Usage),
		"elapsed_ms", elapsed.Milliseconds(),
	)
}

func (lg *Logger) OnToolCall(ctx context.Context, a *agent.Agent, runID string, name, args string) {
	lg.l.Info("tool_call", "agent", a.Name, "run_id", runID, "tool", name, "args", args)
}

func (lg *Logger) OnToolResult(ctx context.Context, a *agent.Agent, runID string, name, result string, err error, elapsed time.Duration) {
	attrs := []any{"agent", a.Name, "run_id", runID, "tool", name}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	attrs = append(attrs, "result", result, "elapsed_ms", elapsed.Milliseconds())
	lg.l.Info("tool_result", attrs...)
}

func (lg *Logger) OnHandoff(ctx context.Context, from, to *agent.Agent, runID string) {
	lg.l.Info("handoff", "agent", from.Name, "run_id", runID, "from", from.Name, "to", to.Name)
}

func (lg *Logger) OnGuardrail(ctx context.Context, a *agent.Agent, runID string, stage agent.GuardrailStage, name string, res agent.GuardrailResult, err error, elapsed time.Duration) {
	attrs := []any{"agent", a.Name, "run_id", runID, "stage", string(stage), "guardrail", name}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	} else {
		attrs = append(attrs, "tripwire", res.Tripwire, "info", res.Info)
	}
	attrs = append(attrs, "elapsed_ms", elapsed.Milliseconds())
	lg.l.Info("guardrail", attrs...)
}

// marshal pre-serializes values so records are stable across slog handlers.
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("audit: unserializable value: %v", err)
	}
	return string(b)
}
