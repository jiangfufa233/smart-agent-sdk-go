package tool

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type policyArgs struct {
	V string `json:"v"`
}

func policyTarget() Tool {
	t, _ := NewFunction("target", "a guarded tool",
		func(_ context.Context, in policyArgs) (string, error) { return "ran:" + in.V, nil })
	return t
}

func TestAllowlist(t *testing.T) {
	guarded := WithPolicy(policyTarget(), Allowlist("target"))
	out, err := guarded.Run(t.Context(), `{"v":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ran:x" {
		t.Errorf("out = %q", out)
	}

	blocked := WithPolicy(policyTarget(), Allowlist("other"))
	_, err = blocked.Run(t.Context(), `{"v":"x"}`)
	if err == nil {
		t.Fatal("expected denial")
	}
	var authErr *AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthorizationError, got %T: %v", err, err)
	}
	if authErr.Tool != "target" {
		t.Errorf("AuthorizationError.Tool = %q", authErr.Tool)
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("message should explain the denial: %v", err)
	}
}

func TestAllowlistEmptyDeniesAll(t *testing.T) {
	guarded := WithPolicy(policyTarget(), Allowlist())
	_, err := guarded.Run(t.Context(), `{"v":"x"}`)
	if err == nil {
		t.Fatal("empty allowlist must deny")
	}
}

func TestDenylist(t *testing.T) {
	guarded := WithPolicy(policyTarget(), Denylist("target"))
	_, err := guarded.Run(t.Context(), `{"v":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected denial, got %v", err)
	}

	guarded = WithPolicy(policyTarget(), Denylist("other"))
	if _, err := guarded.Run(t.Context(), `{"v":"x"}`); err != nil {
		t.Fatalf("non-listed tool should pass: %v", err)
	}
}

func TestAllowAll(t *testing.T) {
	guarded := WithPolicy(policyTarget(), AllowAll)
	if _, err := guarded.Run(t.Context(), `{"v":"x"}`); err != nil {
		t.Fatalf("AllowAll should pass: %v", err)
	}
}

func TestWithPolicyNil(t *testing.T) {
	target := policyTarget()
	if got := WithPolicy(target, nil); got != target {
		t.Fatal("nil policy must return the tool unchanged")
	}
}

func TestPolicySeesArguments(t *testing.T) {
	var seen ToolCall
	p := PolicyFunc(func(_ context.Context, call ToolCall) error {
		seen = call
		return nil
	})
	guarded := WithPolicy(policyTarget(), p)
	if _, err := guarded.Run(t.Context(), `{"v":"abc"}`); err != nil {
		t.Fatal(err)
	}
	if seen.Name != "target" || seen.Description != "a guarded tool" || seen.Arguments != `{"v":"abc"}` {
		t.Errorf("policy saw %+v", seen)
	}
}

func TestPolicyContextPassedThrough(t *testing.T) {
	p := PolicyFunc(func(ctx context.Context, _ ToolCall) error {
		if ctx == nil {
			t.Error("policy received nil context")
		}
		return nil
	})
	if _, err := WithPolicy(policyTarget(), p).Run(t.Context(), `{"v":"x"}`); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizationErrorNotDoubleWrapped(t *testing.T) {
	sentinel := &AuthorizationError{Tool: "target", Err: errors.New("custom reason")}
	guarded := WithPolicy(policyTarget(), PolicyFunc(func(context.Context, ToolCall) error { return sentinel }))
	_, err := guarded.Run(t.Context(), `{"v":"x"}`)
	var authErr *AuthorizationError
	if !errors.As(err, &authErr) || authErr != sentinel {
		t.Fatalf("expected the original error to pass through, got %v", err)
	}
	if !errors.Is(err, sentinel.Err) {
		t.Errorf("Unwrap should expose the reason: %v", err)
	}
}

func TestPolicyDenialBeforeExecution(t *testing.T) {
	executed := false
	target, _ := NewFunction("target", "d",
		func(context.Context, policyArgs) (string, error) { executed = true; return "", nil })
	guarded := WithPolicy(target, Denylist("target"))
	if _, err := guarded.Run(t.Context(), `{"v":"x"}`); err == nil {
		t.Fatal("expected denial")
	}
	if executed {
		t.Error("denied tool must not execute")
	}
}

func TestRequireApprovalBlocksThenAllows(t *testing.T) {
	requests := make(chan ApprovalRequest, 1)
	decisions := make(chan bool)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	executed := make(chan struct{}, 1)
	target, err := NewFunction("target", "a guarded tool",
		func(context.Context, policyArgs) (string, error) {
			executed <- struct{}{}
			return "ran", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	guarded := WithPolicy(target, RequireApproval(ApproverFunc(func(ctx context.Context, request ApprovalRequest) (bool, error) {
		select {
		case requests <- request:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		select {
		case allowed := <-decisions:
			return allowed, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})))

	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := guarded.Run(ctx, `{"v":"x"}`)
		done <- outcome{out: out, err: err}
	}()

	select {
	case request := <-requests:
		want := ApprovalRequest{Tool: "target", Description: "a guarded tool", Arguments: `{"v":"x"}`}
		if request != want {
			t.Fatalf("approval request = %+v, want %+v", request, want)
		}
	case <-time.After(time.Second):
		t.Fatal("approver did not receive a request")
	}
	select {
	case <-executed:
		t.Fatal("tool executed before approval")
	case result := <-done:
		t.Fatalf("tool returned before approval: %+v", result)
	default:
	}

	decisions <- true
	select {
	case result := <-done:
		if result.err != nil || result.out != "ran" {
			t.Fatalf("result = %+v, want ran without error", result)
		}
	case <-time.After(time.Second):
		t.Fatal("approved tool did not return")
	}
	select {
	case <-executed:
	default:
		t.Fatal("approved tool did not execute")
	}
}

func TestRequireApprovalDeniesBeforeExecution(t *testing.T) {
	executed := false
	target, err := NewFunction("target", "a guarded tool",
		func(context.Context, policyArgs) (string, error) {
			executed = true
			return "ran", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	guarded := WithPolicy(target, RequireApproval(ApproverFunc(func(context.Context, ApprovalRequest) (bool, error) {
		return false, nil
	})))

	_, err = guarded.Run(t.Context(), `{"v":"x"}`)
	var authErr *AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %T (%v), want *AuthorizationError", err, err)
	}
	if authErr.Tool != "target" || !strings.Contains(authErr.Error(), "human approval denied") {
		t.Fatalf("authorization error = %v", authErr)
	}
	if executed {
		t.Fatal("denied tool executed")
	}
}

func TestRequireApprovalCancellationDeniesBeforeExecution(t *testing.T) {
	started := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	executed := false
	target, err := NewFunction("target", "a guarded tool",
		func(context.Context, policyArgs) (string, error) {
			executed = true
			return "ran", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	guarded := WithPolicy(target, RequireApproval(ApproverFunc(func(ctx context.Context, _ ApprovalRequest) (bool, error) {
		started <- struct{}{}
		<-ctx.Done()
		return false, ctx.Err()
	})))
	done := make(chan error, 1)
	go func() {
		_, err := guarded.Run(ctx, `{"v":"x"}`)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("approver did not start")
	}
	cancel()
	select {
	case err := <-done:
		var authErr *AuthorizationError
		if !errors.As(err, &authErr) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want AuthorizationError wrapping context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled approval did not return")
	}
	if executed {
		t.Fatal("canceled approval executed the tool")
	}
}

func TestRequireApprovalAllowsAfterCancelDenies(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	executed := false
	target, err := NewFunction("target", "a guarded tool",
		func(context.Context, policyArgs) (string, error) {
			executed = true
			return "ran", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	guarded := WithPolicy(target, RequireApproval(ApproverFunc(func(context.Context, ApprovalRequest) (bool, error) {
		cancel() // the decision arrives only after the context died
		return true, nil
	})))

	_, err = guarded.Run(ctx, `{"v":"x"}`)
	var authErr *AuthorizationError
	if !errors.As(err, &authErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want AuthorizationError wrapping context.Canceled", err)
	}
	if executed {
		t.Fatal("a late approval must not execute the tool")
	}
}

func TestWithPolicyGuardExposesInnerToolAndPolicy(t *testing.T) {
	target := policyTarget()
	policy := PolicyFunc(func(context.Context, ToolCall) error { return nil })
	guarded := WithPolicy(target, policy)
	u, ok := guarded.(interface{ Unwrap() Tool })
	if !ok {
		t.Fatal("guarded tool does not expose Unwrap")
	}
	if u.Unwrap() != target {
		t.Fatal("Unwrap returned the wrong tool")
	}
	p, ok := guarded.(interface{ Policy() Policy })
	if !ok {
		t.Fatal("guarded tool does not expose Policy")
	}
	// PolicyFunc holds a func value, so compare through reflect to avoid a
	// runtime panic on interface comparison.
	if p.Policy() == nil || reflect.ValueOf(p.Policy()).Pointer() != reflect.ValueOf(policy).Pointer() {
		t.Fatal("Policy returned the wrong policy")
	}
}

func TestRequireApprovalTargetsNamedTools(t *testing.T) {
	var requests []ApprovalRequest
	approver := ApproverFunc(func(_ context.Context, request ApprovalRequest) (bool, error) {
		requests = append(requests, request)
		return false, nil
	})
	policy := RequireApproval(approver, "restricted")

	safeRan := false
	safe, err := NewFunction("safe", "safe tool", func(context.Context, policyArgs) (string, error) {
		safeRan = true
		return "safe", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithPolicy(safe, policy).Run(t.Context(), `{"v":"x"}`); err != nil {
		t.Fatalf("unselected tool: %v", err)
	}
	if !safeRan || len(requests) != 0 {
		t.Fatalf("unselected tool state = ran:%t requests:%+v", safeRan, requests)
	}

	restricted, err := NewFunction("restricted", "restricted tool", func(context.Context, policyArgs) (string, error) {
		return "restricted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithPolicy(restricted, policy).Run(t.Context(), `{"v":"x"}`); err == nil {
		t.Fatal("selected tool should require approval")
	}
	if len(requests) != 1 || requests[0].Tool != "restricted" {
		t.Fatalf("approval requests = %+v", requests)
	}
}

func TestPerToolOverridesDefaultPolicy(t *testing.T) {
	var approvals []ApprovalRequest
	policy := PerTool(AllowAll, map[string]Policy{
		"restricted": RequireApproval(ApproverFunc(func(_ context.Context, request ApprovalRequest) (bool, error) {
			approvals = append(approvals, request)
			return false, nil
		})),
	})

	safeRan := false
	safe, err := NewFunction("safe", "safe tool", func(context.Context, policyArgs) (string, error) {
		safeRan = true
		return "safe", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithPolicy(safe, policy).Run(t.Context(), `{"v":"x"}`); err != nil || !safeRan {
		t.Fatalf("default policy result = (%v, ran:%t), want allow", err, safeRan)
	}

	restrictedRan := false
	restricted, err := NewFunction("restricted", "restricted tool", func(context.Context, policyArgs) (string, error) {
		restrictedRan = true
		return "restricted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithPolicy(restricted, policy).Run(t.Context(), `{"v":"x"}`); err == nil {
		t.Fatal("override should require approval")
	}
	if restrictedRan || len(approvals) != 1 || approvals[0].Tool != "restricted" {
		t.Fatalf("override state = ran:%t approvals:%+v", restrictedRan, approvals)
	}
}
