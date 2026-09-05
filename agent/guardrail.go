package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/tracing"
)

// GuardrailStage identifies which run phase a guardrail belongs to.
type GuardrailStage string

const (
	// StageInput guardrails inspect the user input before the first model
	// call.
	StageInput GuardrailStage = "input"
	// StageOutput guardrails inspect the final output before it is returned.
	StageOutput GuardrailStage = "output"
)

// GuardrailResult is a guardrail's verdict.
type GuardrailResult struct {
	// Tripwire reports a policy violation; when true the run fails with a
	// *GuardrailTripwireError.
	Tripwire bool
	// Info carries guardrail-specific details for audit logs and debugging.
	Info any
}

// InputGuardrail inspects the user input of a run. All input guardrails of
// the run's agent run concurrently before the first model call; a tripwire
// fails the run without any model call.
type InputGuardrail struct {
	// Name identifies the guardrail in errors, hooks and audit logs. Empty
	// defaults to "guardrail".
	Name string
	// Guardrail returns the verdict for input. A non-nil error fails the run
	// (fail-closed) and is not treated as a tripwire.
	Guardrail func(ctx context.Context, a *Agent, input string) (GuardrailResult, error)
}

// OutputGuardrail inspects the finished run result before it is returned.
// The output guardrails of the agent that produced the final output apply —
// after a handoff that is the target agent, not the agent the run started
// with.
type OutputGuardrail struct {
	// Name identifies the guardrail in errors, hooks and audit logs. Empty
	// defaults to "guardrail".
	Name string
	// Guardrail returns the verdict for the completed run. A non-nil error
	// fails the run (fail-closed) and is not treated as a tripwire.
	Guardrail func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error)
}

// ErrGuardrailTripwire is the sentinel matched by errors.Is when a guardrail
// trips. Use *GuardrailTripwireError with errors.As for details.
var ErrGuardrailTripwire = errors.New("agent: guardrail tripwire triggered")

// GuardrailTripwireError reports a guardrail that tripped during a run. The
// run was abandoned; Info carries the guardrail-provided details.
type GuardrailTripwireError struct {
	Stage     GuardrailStage
	Guardrail string
	Info      any
}

func (e *GuardrailTripwireError) Error() string {
	return fmt.Sprintf("agent: %s guardrail %q tripped", e.Stage, e.Guardrail)
}

func (e *GuardrailTripwireError) Is(target error) bool { return target == ErrGuardrailTripwire }

// runInputGuardrails runs a's input guardrails concurrently before the first
// model call and fails the run if any of them trips or errors.
func runInputGuardrails(ctx context.Context, a *Agent, input string, hooks Hooks, tracer tracing.Tracer, runID string) error {
	if len(a.InputGuardrails) == 0 {
		return nil
	}
	names := make([]string, len(a.InputGuardrails))
	evals := make([]func(context.Context) (GuardrailResult, error), len(a.InputGuardrails))
	for i, g := range a.InputGuardrails {
		if g.Guardrail == nil {
			return fmt.Errorf("agent: input guardrail %q has no function configured", guardrailName(g.Name))
		}
		names[i] = guardrailName(g.Name)
		evals[i] = func(ctx context.Context) (GuardrailResult, error) { return g.Guardrail(ctx, a, input) }
	}
	return runGuardrails(ctx, StageInput, a, names, evals, hooks, tracer, runID)
}

// runOutputGuardrails runs a's output guardrails on the completed result.
func runOutputGuardrails(ctx context.Context, a *Agent, res *RunResult, hooks Hooks, tracer tracing.Tracer, runID string) error {
	if len(a.OutputGuardrails) == 0 {
		return nil
	}
	names := make([]string, len(a.OutputGuardrails))
	evals := make([]func(context.Context) (GuardrailResult, error), len(a.OutputGuardrails))
	for i, g := range a.OutputGuardrails {
		if g.Guardrail == nil {
			return fmt.Errorf("agent: output guardrail %q has no function configured", guardrailName(g.Name))
		}
		names[i] = guardrailName(g.Name)
		evals[i] = func(ctx context.Context) (GuardrailResult, error) { return g.Guardrail(ctx, a, res) }
	}
	return runGuardrails(ctx, StageOutput, a, names, evals, hooks, tracer, runID)
}

// runGuardrails evaluates every guardrail in its own goroutine and always
// waits for all of them, so audit logs capture the full verdict set. Verdicts
// are then judged in declaration order: errors first (fail-closed), then
// tripwires.
func runGuardrails(ctx context.Context, stage GuardrailStage, a *Agent, names []string, evals []func(context.Context) (GuardrailResult, error), hooks Hooks, tracer tracing.Tracer, runID string) error {
	type verdict struct {
		res GuardrailResult
		err error
	}
	results := make([]verdict, len(evals))
	var wg sync.WaitGroup
	for i := range evals {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spanCtx, span := tracer.Start(ctx, "guardrail."+names[i])
			t0 := time.Now()
			res, err := evals[i](spanCtx)
			elapsed := time.Since(t0)
			if res.Tripwire {
				span.Set("tripwire", "true")
			}
			span.End(nil)
			if gh, ok := hooks.(GuardrailHook); ok {
				gh.OnGuardrail(spanCtx, a, runID, stage, names[i], res, err, elapsed)
			}
			results[i] = verdict{res: res, err: err}
		}(i)
	}
	wg.Wait()

	for i := range results {
		if results[i].err != nil {
			return fmt.Errorf("agent: %s guardrail %q failed: %w", stage, names[i], results[i].err)
		}
	}
	for i := range results {
		if results[i].res.Tripwire {
			return &GuardrailTripwireError{Stage: stage, Guardrail: names[i], Info: results[i].res.Info}
		}
	}
	return nil
}

func guardrailName(name string) string {
	if name == "" {
		return "guardrail"
	}
	return name
}
