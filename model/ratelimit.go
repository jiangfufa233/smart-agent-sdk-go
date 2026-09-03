package model

import (
	"context"
	"sync"
	"time"
)

// RateLimiter is a lazy token bucket: it spawns no background goroutine and
// refills tokens continuously on each Wait call.
type RateLimiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

// NewRateLimiter admits at most rate calls per second, in bursts of burst.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	if rate <= 0 {
		rate = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// Wait blocks until a token is available or ctx is done.
func (l *RateLimiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

type rateLimitedModel struct {
	m       Model
	limiter *RateLimiter
}

// WithRateLimit returns a Model throttled by a token bucket. The limiter is
// shared by all calls through the returned Model and is safe for concurrent
// use.
func WithRateLimit(m Model, rate float64, burst int) Model {
	return &rateLimitedModel{m: m, limiter: NewRateLimiter(rate, burst)}
}

func (r *rateLimitedModel) Chat(ctx context.Context, req *Request) (*Response, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return r.m.Chat(ctx, req)
}
