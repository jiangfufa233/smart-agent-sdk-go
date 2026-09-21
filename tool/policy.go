package tool

import (
	"context"
	"errors"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
)

// ToolCall describes one pending tool invocation, as seen by a Policy.
type ToolCall struct {
	// Name is the tool name.
	Name string
	// Description is the tool's description from its spec.
	Description string
	// Arguments is the raw JSON argument string produced by the model.
	Arguments string
}

// Policy authorizes tool invocations. Authorize returns nil to allow the
// call; a non-nil error denies it. Denial errors are wrapped in an
// *AuthorizationError and their message is fed back to the model, which
// lets the agent adapt instead of aborting the run.
type Policy interface {
	Authorize(ctx context.Context, call ToolCall) error
}

// PolicyFunc adapts a function to Policy. It enables inline rules and
// human-in-the-loop approval flows (block on ctx until a person decides,
// or return an error to deny).
type PolicyFunc func(ctx context.Context, call ToolCall) error

// Authorize implements Policy.
func (f PolicyFunc) Authorize(ctx context.Context, call ToolCall) error {
	return f(ctx, call)
}

// ApprovalRequest describes a tool call waiting for a human decision.
type ApprovalRequest struct {
	// Tool is the requested tool's name.
	Tool string
	// Description is the requested tool's description from its spec.
	Description string
	// Arguments is the raw JSON argument string produced by the model.
	Arguments string
}

// Approver makes a synchronous human approval decision. Approve may block
// while a decision is pending and must return when ctx is canceled.
type Approver interface {
	Approve(ctx context.Context, request ApprovalRequest) (allowed bool, err error)
}

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(ctx context.Context, request ApprovalRequest) (allowed bool, err error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, request ApprovalRequest) (bool, error) {
	return f(ctx, request)
}

// AuthorizationError reports a tool call rejected by a Policy.
type AuthorizationError struct {
	// Tool is the denied tool's name.
	Tool string
	// Err is the underlying denial reason returned by the policy.
	Err error
}

func (e *AuthorizationError) Error() string {
	return fmt.Sprintf("tool %q denied by policy: %v", e.Tool, e.Err)
}

// Unwrap returns the underlying denial reason.
func (e *AuthorizationError) Unwrap() error {
	return e.Err
}

// AllowAll allows every tool call. Use it to make "no restrictions" an
// explicit choice.
var AllowAll Policy = PolicyFunc(func(context.Context, ToolCall) error { return nil })

// Allowlist allows only the named tools; an empty allowlist denies
// everything.
func Allowlist(names ...string) Policy {
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	return PolicyFunc(func(_ context.Context, call ToolCall) error {
		if _, ok := allowed[call.Name]; ok {
			return nil
		}
		return fmt.Errorf("tool %q is not in the allowlist", call.Name)
	})
}

// Denylist blocks the named tools and allows everything else.
func Denylist(names ...string) Policy {
	denied := make(map[string]struct{}, len(names))
	for _, n := range names {
		denied[n] = struct{}{}
	}
	return PolicyFunc(func(_ context.Context, call ToolCall) error {
		if _, ok := denied[call.Name]; ok {
			return fmt.Errorf("tool %q is denied", call.Name)
		}
		return nil
	})
}

// RequireApproval requires approver to allow each selected call before it can
// run. With no tool names, it requires approval for every tool. An approver
// error, a denial, a missing approver, or a canceled context denies the call.
func RequireApproval(approver Approver, toolNames ...string) Policy {
	selected := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		selected[name] = struct{}{}
	}

	return PolicyFunc(func(ctx context.Context, call ToolCall) error {
		if len(selected) > 0 {
			if _, ok := selected[call.Name]; !ok {
				return nil
			}
		}
		if err := ctx.Err(); err != nil {
			return approvalAuthorizationError(call.Name, err)
		}
		if approver == nil {
			return approvalAuthorizationError(call.Name, errors.New("no approver configured"))
		}

		allowed, err := approver.Approve(ctx, ApprovalRequest{
			Tool:        call.Name,
			Description: call.Description,
			Arguments:   call.Arguments,
		})
		if err != nil {
			return approvalAuthorizationError(call.Name, err)
		}
		if !allowed {
			return approvalAuthorizationError(call.Name, errors.New("human approval denied"))
		}
		// The context may have been canceled while the decision was pending;
		// an approval that arrives after cancellation does not authorize a run.
		if err := ctx.Err(); err != nil {
			return approvalAuthorizationError(call.Name, err)
		}
		return nil
	})
}

func approvalAuthorizationError(toolName string, err error) error {
	var authErr *AuthorizationError
	if errors.As(err, &authErr) {
		return err
	}
	return &AuthorizationError{Tool: toolName, Err: err}
}

// PerTool selects a policy for each tool name. Overrides replace defaultPolicy
// for their matching tools. A nil default or nil override allows the call.
// Overrides are copied so changes to the caller's map do not affect the policy.
func PerTool(defaultPolicy Policy, overrides map[string]Policy) Policy {
	byName := make(map[string]Policy, len(overrides))
	for name, policy := range overrides {
		byName[name] = policy
	}
	return PolicyFunc(func(ctx context.Context, call ToolCall) error {
		policy, ok := byName[call.Name]
		if !ok {
			policy = defaultPolicy
		}
		if policy == nil {
			return nil
		}
		return policy.Authorize(ctx, call)
	})
}

// WithPolicy guards t with p: every Run consults p.Authorize before
// executing the tool. A nil p returns t unchanged. Specs pass through
// unchanged, so the model still sees the tool and can be told why a call
// was denied.
func WithPolicy(t Tool, p Policy) Tool {
	if p == nil {
		return t
	}
	return guardedTool{t: t, p: p}
}

type guardedTool struct {
	t Tool
	p Policy
}

// Spec implements Tool.
func (g guardedTool) Spec() model.ToolParam {
	return g.t.Spec()
}

// Unwrap exposes the guarded tool. Runner uses it to keep recognizing
// delegated tools behind a guard and to recover the policy for delegation
// calls that bypass Run.
func (g guardedTool) Unwrap() Tool { return g.t }

// Policy returns the authorization policy applied by this guard.
func (g guardedTool) Policy() Policy { return g.p }

// Run implements Tool.
func (g guardedTool) Run(ctx context.Context, argumentsJSON string) (string, error) {
	spec := g.t.Spec()
	call := ToolCall{
		Name:        spec.Function.Name,
		Description: spec.Function.Description,
		Arguments:   argumentsJSON,
	}
	if err := g.p.Authorize(ctx, call); err != nil {
		var authErr *AuthorizationError
		if errors.As(err, &authErr) {
			return "", err
		}
		return "", &AuthorizationError{Tool: call.Name, Err: err}
	}
	return g.t.Run(ctx, argumentsJSON)
}
