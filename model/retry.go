package model

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

// RetryPolicy configures WithRetry. The zero value performs a single attempt
// with no backoff.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first call.
	// Values <= 1 disable retries.
	MaxAttempts int
	// InitialBackoff is the sleep before the second attempt. Zero means no
	// sleep between attempts.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential growth of the backoff. Zero means no cap.
	MaxBackoff time.Duration
	// Multiplier grows the backoff per attempt; values <= 1 keep it flat.
	Multiplier float64
	// Jitter randomizes each sleep by ±Jitter (fraction; 0.2 = ±20%).
	Jitter float64
	// RetryOn overrides the default classification (ModelError.Retryable)
	// when non-nil.
	RetryOn func(error) bool
}

// DefaultRetryPolicy retries twice with exponential backoff and jitter.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    3,
		InitialBackoff: 200 * time.Millisecond,
		MaxBackoff:     10 * time.Second,
		Multiplier:     2,
		Jitter:         0.2,
	}
}

// WithRetry returns a Model that retries retryable failures with capped
// exponential backoff. It composes with Fallback as retry-inside,
// fallback-outside: wrap each candidate with WithRetry and pass all of them
// to Fallback.
func WithRetry(m Model, p RetryPolicy) Model {
	return &retryModel{m: m, p: p}
}

type retryModel struct {
	m Model
	p RetryPolicy
}

func (r *retryModel) Chat(ctx context.Context, req *Request) (*Response, error) {
	max := r.p.MaxAttempts
	if max <= 0 {
		max = 1
	}
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := r.m.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt >= max || !r.shouldRetry(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.backoffFor(attempt)):
		}
	}
	return nil, lastErr
}

func (r *retryModel) shouldRetry(err error) bool {
	if r.p.RetryOn != nil {
		return r.p.RetryOn(err)
	}
	var me *ModelError
	return errors.As(err, &me) && me.Retryable
}

func (r *retryModel) backoffFor(attempt int) time.Duration {
	if r.p.InitialBackoff <= 0 {
		return 0
	}
	mult := r.p.Multiplier
	if mult <= 1 {
		mult = 1
	}
	d := float64(r.p.InitialBackoff) * math.Pow(mult, float64(attempt-1))
	if r.p.MaxBackoff > 0 {
		d = math.Min(d, float64(r.p.MaxBackoff))
	}
	if j := r.p.Jitter; j > 0 {
		d *= 1 - j + 2*j*rand.Float64()
	}
	return time.Duration(d)
}
