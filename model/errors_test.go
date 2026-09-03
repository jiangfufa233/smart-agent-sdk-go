package model

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassifyHTTPStatus(t *testing.T) {
	cases := []struct {
		status    int
		kind      ErrorKind
		retryable bool
	}{
		{401, ErrorAuth, false},
		{403, ErrorAuth, false},
		{400, ErrorInvalidRequest, false},
		{404, ErrorInvalidRequest, false},
		{422, ErrorInvalidRequest, false},
		{429, ErrorRateLimited, true},
		{408, ErrorRateLimited, true},
		{500, ErrorServerError, true},
		{503, ErrorServerError, true},
		{418, ErrorInvalidRequest, false},
	}
	for _, tc := range cases {
		kind, retryable := ClassifyHTTPStatus(tc.status)
		if kind != tc.kind || retryable != tc.retryable {
			t.Errorf("status %d: got (%s,%v), want (%s,%v)", tc.status, kind, retryable, tc.kind, tc.retryable)
		}
	}
}

func TestNewHTTPErrorTruncatesBody(t *testing.T) {
	body := strings.Repeat("x", 4096)
	e := NewHTTPError("openai", 500, body)
	if len(e.Body) != 2048 {
		t.Fatalf("body not truncated: %d", len(e.Body))
	}
	if e.Kind != ErrorServerError || !e.Retryable {
		t.Fatalf("classification wrong: %+v", e)
	}
}

func TestClassifyTransportError(t *testing.T) {
	if err := ClassifyTransportError("p", nil); err != nil {
		t.Fatalf("nil input: %v", err)
	}

	canceled := ClassifyTransportError("p", context.Canceled)
	if !errors.Is(canceled, context.Canceled) {
		t.Fatalf("canceled must pass through: %v", canceled)
	}
	var me *ModelError
	if errors.As(canceled, &me) {
		t.Fatalf("canceled must not be wrapped as ModelError: %v", canceled)
	}

	timeout := ClassifyTransportError("p", context.DeadlineExceeded)
	if !errors.As(timeout, &me) || me.Kind != ErrorTimeout || !me.Retryable {
		t.Fatalf("deadline should be retryable timeout: %v", timeout)
	}

	plain := ClassifyTransportError("p", errors.New("conn reset"))
	if !errors.As(plain, &me) || me.Kind != ErrorNetwork || !me.Retryable {
		t.Fatalf("plain error should be retryable network: %v", plain)
	}
}

func TestModelErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &ModelError{Kind: ErrorServerError, Retryable: true, Provider: "p", Err: inner}
	if !errors.Is(e, inner) {
		t.Fatal("unwrap chain broken")
	}
	if got := e.Error(); got == "" {
		t.Fatal("empty message")
	}
}
