package agent

import (
	"errors"
	"fmt"
)

// ErrMaxTurns is the sentinel matched by errors.Is when a run exhausts its
// turn budget. Use *MaxTurnsError with errors.As for details.
var ErrMaxTurns = errors.New("agent: max turns exceeded")

// MaxTurnsError reports the configured budget that was exhausted.
type MaxTurnsError struct{ MaxTurns int }

func (e *MaxTurnsError) Error() string {
	return fmt.Sprintf("agent: max turns exceeded (%d)", e.MaxTurns)
}

func (e *MaxTurnsError) Is(target error) bool { return target == ErrMaxTurns }

// ToolError records a tool invocation that failed during a run. The failure
// text is still fed back to the model as part of the conversation.
type ToolError struct {
	Tool      string
	Arguments string
	Err       error
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("agent: tool %q failed: %v", e.Tool, e.Err)
}

func (e *ToolError) Unwrap() error { return e.Err }
