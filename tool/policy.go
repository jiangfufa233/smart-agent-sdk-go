package tool

import (
	"context"
	"errors"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
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
