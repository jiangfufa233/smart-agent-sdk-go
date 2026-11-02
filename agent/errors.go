package agent

import (
	"errors"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
)

// ErrMaxTurns is the sentinel matched by errors.Is when a run exhausts its
// turn budget. Use *MaxTurnsError with errors.As for details.
var ErrMaxTurns = errors.New("agent: max turns exceeded")

// MaxTurnsError reports the configured budget that was exhausted.
//
// Messages holds everything the failed run produced after the history it
// started from: the user input followed by the assistant and tool messages
// generated in response, in request order. It is exactly the slice a
// successful run would have persisted with Session.Append, so a caller can
// carry the conversation forward by appending it to the same session and
// starting a new run.
type MaxTurnsError struct {
	MaxTurns int
	Messages []model.Message
}

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
