// Package tracing defines the minimal span interfaces used to instrument
// runs. The default tracer is a no-op; NewSlog logs spans to a structured
// logger, and OpenTelemetry adapters can implement the same two interfaces
// without the SDK taking on an otel dependency.
package tracing

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// Span is one instrumented operation.
type Span interface {
	// Set annotates the span with a key/value pair.
	Set(key string, value any)
	// End completes the span; err, when non-nil, marks it failed.
	End(err error)
}

// Tracer creates spans. Implementations must be safe for concurrent use.
type Tracer interface {
	// Start opens a span named name and returns a context carrying it.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// Nop returns a tracer that discards spans.
func Nop() Tracer { return nopTracer{} }

type nopTracer struct{}

func (nopTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, nopSpan{}
}

type nopSpan struct{}

func (nopSpan) Set(string, any) {}
func (nopSpan) End(error)       {}

type ctxKey struct{}

// SpanFromContext returns the active span, or a no-op span when none is set.
func SpanFromContext(ctx context.Context) Span {
	if s, ok := ctx.Value(ctxKey{}).(Span); ok {
		return s
	}
	return nopSpan{}
}

// NewSlog returns a tracer that logs each span at Debug level when it ends.
func NewSlog(l *slog.Logger) Tracer { return slogTracer{l: l} }

type slogTracer struct{ l *slog.Logger }

func (t slogTracer) Start(ctx context.Context, name string) (context.Context, Span) {
	s := &slogSpan{l: t.l, name: name, start: time.Now(), attrs: map[string]any{}}
	return context.WithValue(ctx, ctxKey{}, s), s
}

type slogSpan struct {
	l     *slog.Logger
	name  string
	start time.Time

	mu    sync.Mutex
	attrs map[string]any
}

func (s *slogSpan) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs[key] = value
}

func (s *slogSpan) End(err error) {
	s.mu.Lock()
	attrs := maps.Clone(s.attrs)
	s.mu.Unlock()

	args := make([]any, 0, 2*len(attrs)+4)
	for k, v := range attrs {
		args = append(args, k, v)
	}
	args = append(args, "elapsed_ms", time.Since(s.start).Milliseconds())
	if err != nil {
		args = append(args, "error", err.Error())
	}
	s.l.Debug("trace span", append([]any{"name", s.name}, args...)...)
}
