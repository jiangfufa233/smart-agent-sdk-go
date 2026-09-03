package mcp

import (
	"errors"
	"testing"
)

func TestConnectNotImplemented(t *testing.T) {
	c, err := NewClient(Config{Transport: TransportStdio, Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if _, err := c.Tools(t.Context()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close should be a no-op, got %v", err)
	}
}
