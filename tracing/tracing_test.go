package tracing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogTracerLogsSpanEnd(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := NewSlog(l)

	ctx, span := tr.Start(context.Background(), "op")
	span.Set("key", 42)
	span.End(errors.New("failed"))
	_ = ctx

	out := buf.String()
	for _, want := range []string{`name=op`, `key=42`, `error=failed`, `elapsed_ms=`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

func TestSlogTracerSuccess(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := NewSlog(l)

	_, span := tr.Start(context.Background(), "ok-op")
	span.End(nil)

	if strings.Contains(buf.String(), "error=") {
		t.Errorf("successful span must not log error: %s", buf.String())
	}
}

func TestNopTracer(t *testing.T) {
	ctx, span := Nop().Start(context.Background(), "op")
	span.Set("k", "v")
	span.End(nil)
	if got := SpanFromContext(ctx); got == nil {
		t.Fatal("SpanFromContext must never return nil")
	}
	if SpanFromContext(context.Background()) == nil {
		t.Fatal("SpanFromContext must never return nil")
	}
}
