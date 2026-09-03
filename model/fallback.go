package model

import (
	"context"
	"errors"
	"fmt"
)

// Fallback returns a Model that tries each candidate in order. It advances to
// the next candidate on auth, rate-limit, server, timeout and network
// failures (a misconfigured primary should not take down the chain), and
// stops on invalid-request / protocol failures, which switching providers
// cannot fix. All failures are joined into the returned error; inspect with
// errors.Is / errors.As.
func Fallback(models ...Model) Model {
	return ModelFunc(func(ctx context.Context, req *Request) (*Response, error) {
		if len(models) == 0 {
			return nil, &ModelError{
				Kind: ErrorInvalidRequest,
				Err:  errors.New("model/fallback: no models configured"),
			}
		}
		var errs []error
		for i, m := range models {
			resp, err := m.Chat(ctx, req)
			if err == nil {
				return resp, nil
			}
			errs = append(errs, fmt.Errorf("model[%d]: %w", i, err))
			if ctx.Err() != nil {
				return nil, errors.Join(errs...)
			}
			if !switchable(err) {
				break
			}
		}
		return nil, errors.Join(errs...)
	})
}

func switchable(err error) bool {
	var me *ModelError
	if !errors.As(err, &me) {
		return true
	}
	switch me.Kind {
	case ErrorInvalidRequest, ErrorProtocol:
		return false
	default:
		return true
	}
}
