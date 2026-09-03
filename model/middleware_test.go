package model_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

func chatReq() *model.Request {
	return &model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}}
}

func ptrText(s string) *model.Response {
	return &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: s},
		FinishReason: "stop",
	}
}

func TestRetrySucceedsAfterTransient(t *testing.T) {
	s := testutil.NewScripted(
		testutil.HTTPErrorStep(500, "boom"),
		testutil.HTTPErrorStep(429, "slow down"),
		testutil.TextStep("ok"),
	)
	res, err := model.WithRetry(s, model.RetryPolicy{MaxAttempts: 3}).Chat(context.Background(), chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != "ok" {
		t.Fatalf("unexpected content: %+v", res)
	}
	if s.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", s.Calls())
	}
}

func TestRetryExhausted(t *testing.T) {
	s := testutil.NewScripted(
		testutil.HTTPErrorStep(500, "a"),
		testutil.HTTPErrorStep(500, "b"),
		testutil.HTTPErrorStep(500, "c"),
	)
	_, err := model.WithRetry(s, model.RetryPolicy{MaxAttempts: 3}).Chat(context.Background(), chatReq())
	if err == nil {
		t.Fatal("expected error")
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorServerError || !me.Retryable {
		t.Fatalf("expected retryable server error, got %v", err)
	}
	if s.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", s.Calls())
	}
}

func TestRetryNonRetryable(t *testing.T) {
	s := testutil.NewScripted(testutil.HTTPErrorStep(401, "no key"))
	_, err := model.WithRetry(s, model.RetryPolicy{MaxAttempts: 3}).Chat(context.Background(), chatReq())
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorAuth || me.Retryable {
		t.Fatalf("expected non-retryable auth error, got %v", err)
	}
	if s.Calls() != 1 {
		t.Fatalf("auth errors must not be retried, calls = %d", s.Calls())
	}
}

func TestRetryStopsOnContextCancelDuringBackoff(t *testing.T) {
	s := testutil.NewScripted(testutil.HTTPErrorStep(500, "x"))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	defer cancel()
	_, err := model.WithRetry(s, model.RetryPolicy{MaxAttempts: 3, InitialBackoff: 50 * time.Millisecond}).
		Chat(ctx, chatReq())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if s.Calls() != 1 {
		t.Fatalf("calls = %d, want 1", s.Calls())
	}
}

func TestRetryDeadContext(t *testing.T) {
	s := testutil.NewScripted(testutil.TextStep("never"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := model.WithRetry(s, model.RetryPolicy{MaxAttempts: 3}).Chat(ctx, chatReq())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if s.Calls() != 0 {
		t.Fatalf("dead context must not reach the model, calls = %d", s.Calls())
	}
}

func TestWithTimeout(t *testing.T) {
	s := testutil.NewScripted(testutil.Step{Delay: 100 * time.Millisecond, Resp: ptrText("late")})
	_, err := model.WithTimeout(s, 10*time.Millisecond).Chat(context.Background(), chatReq())
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorTimeout || !me.Retryable {
		t.Fatalf("expected retryable timeout, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout must wrap DeadlineExceeded, got %v", err)
	}
}

func TestFallbackSwitchesOnRetryable(t *testing.T) {
	primary := testutil.NewScripted(testutil.HTTPErrorStep(500, "down"))
	secondary := testutil.NewScripted(testutil.TextStep("ok"))
	res, err := model.Fallback(primary, secondary).Chat(context.Background(), chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != "ok" {
		t.Fatalf("unexpected content: %+v", res)
	}
	if primary.Calls() != 1 || secondary.Calls() != 1 {
		t.Fatalf("primary=%d secondary=%d", primary.Calls(), secondary.Calls())
	}
}

func TestFallbackStopsOnInvalidRequest(t *testing.T) {
	primary := testutil.NewScripted(testutil.HTTPErrorStep(400, "bad"))
	secondary := testutil.NewScripted(testutil.TextStep("ok"))
	_, err := model.Fallback(primary, secondary).Chat(context.Background(), chatReq())
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", err)
	}
	if secondary.Calls() != 0 {
		t.Fatalf("invalid_request must stop the chain, secondary calls = %d", secondary.Calls())
	}
}

func TestFallbackAllFail(t *testing.T) {
	a := testutil.NewScripted(testutil.HTTPErrorStep(500, "a"))
	b := testutil.NewScripted(testutil.HTTPErrorStep(503, "b"))
	_, err := model.Fallback(a, b).Chat(context.Background(), chatReq())
	if err == nil {
		t.Fatal("expected joined error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "model[0]") || !strings.Contains(msg, "model[1]") {
		t.Fatalf("joined error should reference every candidate: %v", msg)
	}
}

func TestRateLimiterBurst(t *testing.T) {
	l := model.NewRateLimiter(1000, 2)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("high-rate waits should be immediate, took %s", elapsed)
	}
}

func TestWithRateLimitThrottles(t *testing.T) {
	m := model.WithRateLimit(model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		return ptrText("ok"), nil
	}), 25, 1) // 40ms per call after the burst token
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := m.Chat(context.Background(), chatReq()); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("rate limiter did not throttle, elapsed %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("rate limiter overshot, elapsed %s", elapsed)
	}
}

func TestRateLimitWaitRespectsContext(t *testing.T) {
	l := model.NewRateLimiter(0.001, 1)
	if err := l.Wait(context.Background()); err != nil { // consume the burst token
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
