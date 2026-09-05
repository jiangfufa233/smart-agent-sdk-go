package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// Session persists the working context of a conversation across Run calls,
// so multi-turn dialogue does not require manually passing res.Messages
// around. Implementations store the full transcript; history compression
// (see HistoryCompressor) only shrinks the view sent to the model.
//
// The system prompt is never stored in a session: it is Agent
// configuration, prepended fresh on every run.
type Session interface {
	// GetItems returns the most recent limit messages in chronological
	// order; limit <= 0 returns the full history.
	GetItems(ctx context.Context, limit int) ([]model.Message, error)
	// AddItems appends messages to the end of the stored history.
	AddItems(ctx context.Context, items []model.Message) error
	// Clear removes all stored messages for this session.
	Clear(ctx context.Context) error
}

// HistoryCompressor shrinks a loaded history before it is sent to the
// model. It receives the full history (possibly with leading system
// messages) and returns the view the model should see. Implementations
// must keep any leading system messages and return the input unchanged
// when no compression is needed. Compression is view-level only: whatever
// a Session stores stays lossless.
type HistoryCompressor interface {
	Compress(ctx context.Context, history []model.Message) ([]model.Message, error)
}

// RunWithSession loads the conversation history from s, runs the agent
// loop with input, and — on success — appends the messages this run
// produced (the user input plus everything generated after it) back to s.
// A failed run writes nothing. AddItems failures fail the run: not
// persisting means the next run would silently lose context.
func (r *Runner) RunWithSession(ctx context.Context, a *Agent, s Session, input string) (*RunResult, error) {
	if s == nil {
		return nil, errors.New("agent: session is nil")
	}
	items, err := s.GetItems(ctx, -1)
	if err != nil {
		return nil, fmt.Errorf("agent: load session: %w", err)
	}
	return r.run(ctx, a, withInstructions(a, items), input, s)
}

// RunStreamWithSession is the streaming counterpart of RunWithSession. The
// session is loaded synchronously; a load failure surfaces as a StreamRun
// whose only event is StreamRunError.
func (r *Runner) RunStreamWithSession(ctx context.Context, a *Agent, s Session, input string) *StreamRun {
	if s == nil {
		return errorStreamRun(errors.New("agent: session is nil"))
	}
	items, err := s.GetItems(ctx, -1)
	if err != nil {
		return errorStreamRun(fmt.Errorf("agent: load session: %w", err))
	}
	return r.runStreamAsync(ctx, a, withInstructions(a, items), input, s)
}

// withInstructions prepends the agent instructions as a system message
// unless the history already starts with one.
func withInstructions(a *Agent, history []model.Message) []model.Message {
	if a.Instructions != "" && (len(history) == 0 || history[0].Role != model.RoleSystem) {
		return append([]model.Message{{Role: model.RoleSystem, Content: a.Instructions}}, history...)
	}
	return history
}

// errorStreamRun returns a StreamRun already terminated with err.
func errorStreamRun(err error) *StreamRun {
	ch := make(chan StreamEvent, 1)
	done := make(chan struct{})
	ch <- StreamEvent{Type: StreamRunError, Err: err}
	close(ch)
	close(done)
	return &StreamRun{Events: ch, done: done, err: err}
}

// compressHistory applies the Runner's compressor to the history that is
// about to be sent to the model. It is a no-op without a compressor or an
// empty history.
func (r *Runner) compressHistory(ctx context.Context, opts runOpts, history []model.Message) ([]model.Message, error) {
	if r.Compressor == nil || len(history) == 0 {
		return history, nil
	}
	spanCtx, span := opts.tracer.Start(ctx, "history.compress")
	out, err := r.Compressor.Compress(spanCtx, history)
	span.End(err)
	if err != nil {
		return nil, fmt.Errorf("agent: compress history: %w", err)
	}
	return out, nil
}

// persistTurn appends the messages this run generated to the session:
// everything after the history the loop started from (the user input plus
// the assistant/tool messages produced in response). historyLen must be
// the length of the history slice the loop received. A no-op when sess is
// nil (plain Run / RunWithHistory).
func persistTurn(ctx context.Context, sess Session, res *RunResult, historyLen int) error {
	if sess == nil {
		return nil
	}
	if err := sess.AddItems(ctx, res.Messages[historyLen:]); err != nil {
		return fmt.Errorf("agent: persist session: %w", err)
	}
	return nil
}
