package agent

import (
	"context"
	"errors"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
)

// DelegatedTool is an optional tool extension recognized by Runner. It keeps
// tool.Tool unchanged while allowing a streaming parent run to execute a child
// agent with task-scoped event forwarding.
type DelegatedTool interface {
	tool.Tool
	DelegationTarget() *Agent
	DelegationRunner() *Runner
	DelegationInput(argumentsJSON string) (string, error)
}

// delegateOf unwraps authorization guards (tool.WithPolicy) around a
// delegated tool so guarding a delegation keeps both its policy and its
// task-scoped streaming. The returned policy is the outermost guard found;
// nil means the delegation is unguarded.
func delegateOf(t tool.Tool) (DelegatedTool, tool.Policy, bool) {
	var policy tool.Policy
	for {
		if d, ok := t.(DelegatedTool); ok {
			return d, policy, true
		}
		guard, ok := t.(interface {
			Unwrap() tool.Tool
			Policy() tool.Policy
		})
		if !ok {
			return nil, nil, false
		}
		if policy == nil {
			policy = guard.Policy()
		}
		t = guard.Unwrap()
	}
}

// authorizeDelegation applies a guard's policy to a delegation call, using the
// same ToolCall shape and denial wrapping as guarded runs.
func authorizeDelegation(ctx context.Context, policy tool.Policy, d DelegatedTool, arguments string) error {
	spec := d.Spec()
	err := policy.Authorize(ctx, tool.ToolCall{
		Name:        spec.Function.Name,
		Description: spec.Function.Description,
		Arguments:   arguments,
	})
	if err == nil {
		return nil
	}
	var authErr *tool.AuthorizationError
	if errors.As(err, &authErr) {
		return authErr
	}
	return &tool.AuthorizationError{Tool: spec.Function.Name, Err: err}
}

func (r *Runner) execDelegatedTool(ctx context.Context, t DelegatedTool, arguments string, opts runOpts) (string, *ToolError) {
	input, err := t.DelegationInput(arguments)
	if err != nil {
		return "error: delegated task arguments: " + err.Error(), &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: err}
	}
	childRunner := t.DelegationRunner()
	if childRunner == nil {
		childRunner = NewRunner()
	}
	childOpts := childRunner.runOpts()
	childOpts.task = TaskRef{
		ID:          newRunID(),
		ParentID:    opts.task.ID,
		RootRunID:   opts.task.RootRunID,
		SourceRunID: childOpts.runID,
	}
	res, err := childRunner.runWithOpts(ctx, t.DelegationTarget(), withInstructions(t.DelegationTarget(), nil), input, nil, childOpts)
	if err != nil {
		return "error: delegated task failed: " + err.Error(), &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: err}
	}
	return truncateOutput(res.Output, opts.maxOutput), nil
}

func (r *Runner) streamDelegatedTool(ctx, emitCtx context.Context, t DelegatedTool, arguments string, turn int, opts runOpts, ch chan<- StreamEvent) (string, *ToolError) {
	input, err := t.DelegationInput(arguments)
	if err != nil {
		return "error: delegated task arguments: " + err.Error(), &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: err}
	}
	target := t.DelegationTarget()
	if err := validateAgent(target); err != nil {
		return "error: delegated task failed: " + err.Error(), &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: err}
	}
	childRunner := t.DelegationRunner()
	if childRunner == nil {
		childRunner = NewRunner()
	}
	childOpts := childRunner.runOpts()
	childOpts.task = TaskRef{
		ID:          newRunID(),
		ParentID:    opts.task.ID,
		RootRunID:   opts.task.RootRunID,
		SourceRunID: childOpts.runID,
	}

	started := streamEventWithTask(childOpts.task, target, StreamTaskStarted, turn)
	if !sendEvent(emitCtx, ch, started) {
		return "", &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: ctx.Err()}
	}
	child := childRunner.runStreamAsyncWithOpts(ctx, target, withInstructions(target, nil), input, nil, childOpts)
	for event := range child.Events {
		switch event.Type {
		case StreamRunStarted, StreamFinalOutput, StreamRunError:
			continue
		}
		if !sendEvent(emitCtx, ch, event) {
			_, _ = child.Wait()
			return "", &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: ctx.Err()}
		}
	}
	res, err := child.Result()
	if err != nil {
		failed := streamEventWithTask(childOpts.task, target, StreamTaskFailed, turn)
		failed.Err = err
		if !sendEvent(emitCtx, ch, failed) {
			return "", &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: ctx.Err()}
		}
		return "error: delegated task failed: " + err.Error(), &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: err}
	}
	completed := streamEventWithTask(childOpts.task, target, StreamTaskCompleted, turn)
	completed.Text = res.Output
	if !sendEvent(emitCtx, ch, completed) {
		return "", &ToolError{Tool: t.Spec().Function.Name, Arguments: arguments, Err: ctx.Err()}
	}
	return truncateOutput(res.Output, opts.maxOutput), nil
}
