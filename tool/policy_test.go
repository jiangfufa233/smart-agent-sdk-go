package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
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
