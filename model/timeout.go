package model

import (
	"context"
	"errors"
	"time"
)

// WithTimeout returns a Model that bounds each call with a per-attempt
// deadline. Deadline failures are classified as retryable *ModelError with
// Kind ErrorTimeout, so WithRetry stacked outside can retry them; the
// caller's own context deadline is never masked.
func WithTimeout(m Model, d time.Duration) Model {
	return ModelFunc(func(ctx context.Context, req *Request) (*Response, error) {
		tctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		resp, err := m.Chat(tctx, req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, &ModelError{Kind: ErrorTimeout, Retryable: true, Err: err}
			}
			return nil, err
		}
		return resp, nil
	})
}
