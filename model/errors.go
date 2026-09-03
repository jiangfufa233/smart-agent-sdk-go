package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrorKind classifies provider failures.
type ErrorKind string

const (
	ErrorAuth           ErrorKind = "auth"            // credentials rejected; not retryable
	ErrorInvalidRequest ErrorKind = "invalid_request" // request problem; not retryable
	ErrorRateLimited    ErrorKind = "rate_limited"    // 429; retryable
	ErrorServerError    ErrorKind = "server_error"    // 5xx; retryable
	ErrorTimeout        ErrorKind = "timeout"         // deadline exceeded; retryable
	ErrorNetwork        ErrorKind = "network"         // transport failure; retryable
	ErrorProtocol       ErrorKind = "protocol"        // undecodable response; not retryable
)

// ModelError is the typed error every provider adapter must return. Use
// errors.As to inspect Kind / Retryable from a generic error value.
type ModelError struct {
	Kind       ErrorKind
	Retryable  bool
	StatusCode int // HTTP status, 0 for transport-level failures
	Provider   string
	Body       string // truncated response body, if any
	Err        error
}

func (e *ModelError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "model/%s: %s", e.Provider, e.Kind)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (status %d)", e.StatusCode)
	}
	if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *ModelError) Unwrap() error { return e.Err }

// ClassifyHTTPStatus maps an HTTP status code to its error kind and default
// retryability.
func ClassifyHTTPStatus(status int) (ErrorKind, bool) {
	switch {
	case status == 401 || status == 403:
		return ErrorAuth, false
	case status == 408 || status == 429:
		return ErrorRateLimited, true
	case status == 400 || status == 404 || status == 405 || status == 413 || status == 422:
		return ErrorInvalidRequest, false
	case status >= 500:
		return ErrorServerError, true
	default:
		return ErrorInvalidRequest, false
	}
}

// NewHTTPError builds a ModelError for a failed HTTP response. body is
// truncated to 2 KiB.
func NewHTTPError(provider string, status int, body string) *ModelError {
	kind, retryable := ClassifyHTTPStatus(status)
	if len(body) > 2048 {
		body = body[:2048]
	}
	return &ModelError{
		Kind:       kind,
		Retryable:  retryable,
		StatusCode: status,
		Provider:   provider,
		Body:       body,
	}
}

// ClassifyTransportError wraps a transport-level failure.
// context.Canceled is returned unchanged: the caller aborted the request, so
// retrying it would be wrong.
func ClassifyTransportError(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ModelError{Kind: ErrorTimeout, Retryable: true, Provider: provider, Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &ModelError{Kind: ErrorTimeout, Retryable: true, Provider: provider, Err: err}
	}
	return &ModelError{Kind: ErrorNetwork, Retryable: true, Provider: provider, Err: err}
}
