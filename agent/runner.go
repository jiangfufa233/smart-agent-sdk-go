package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/tool"
)

// ErrMaxTurns is returned when the runner exhausts its turn budget without
// the model producing a final answer.
var ErrMaxTurns = errors.New("agent: max turns exceeded")

const defaultMaxTurns = 10

// Runner executes the agent loop: call the model, execute any requested
// tool calls, feed results back, repeat until a final answer is produced.
type Runner struct {
	// MaxTurns bounds the number of model calls per run (default 10).
	MaxTurns int
}

// NewRunner returns a Runner with default settings.
func NewRunner() *Runner {
	return &Runner{MaxTurns: defaultMaxTurns}
}

// RunResult is the outcome of a run.
type RunResult struct {
	// Output is the agent's final textual answer.
	Output string
	// FinalMessage is the model message that ended the run.
	FinalMessage model.Message
	// Messages is the full conversation including system prompt, user input,
	// tool calls and tool results. It can be passed back to RunWithHistory
	// for multi-turn conversations.
	Messages []model.Message
}

// Run starts a fresh conversation with a and sends it input.
func (r *Runner) Run(ctx context.Context, a *Agent, input string) (*RunResult, error) {
	var history []model.Message
	if a.Instructions != "" {
		history = append(history, model.Message{Role: model.RoleSystem, Content: a.Instructions})
	}
	return r.run(ctx, a, history, input)
}

// RunWithHistory continues an existing conversation (e.g. the Messages slice
// from a previous RunResult) with a new user input.
func (r *Runner) RunWithHistory(ctx context.Context, a *Agent, history []model.Message, input string) (*RunResult, error) {
	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	if a.Instructions != "" && (len(msgs) == 0 || msgs[0].Role != model.RoleSystem) {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: a.Instructions}}, msgs...)
	}
	return r.run(ctx, a, msgs, input)
}

func (r *Runner) run(ctx context.Context, a *Agent, history []model.Message, input string) (*RunResult, error) {
	if a.Model == nil {
		return nil, errors.New("agent: agent has no model configured")
	}
	maxTurns := r.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	msgs := make([]model.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: input})

	specs := make([]model.ToolParam, 0, len(a.Tools))
	byName := make(map[string]tool.Tool, len(a.Tools))
	for _, t := range a.Tools {
		spec := t.Spec()
		specs = append(specs, spec)
		byName[spec.Function.Name] = t
	}

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := a.Model.Chat(ctx, &model.Request{
			Model:    a.ModelName,
			Messages: msgs,
			Tools:    specs,
		})
		if err != nil {
			return nil, fmt.Errorf("agent: model call failed: %w", err)
		}

		msg := resp.Message
		if len(msg.ToolCalls) == 0 {
			msgs = append(msgs, msg)
			return &RunResult{Output: msg.Content, FinalMessage: msg, Messages: msgs}, nil
		}

		// Assistant message requesting tool calls, then one result per call.
		msgs = append(msgs, msg)
		for _, tc := range msg.ToolCalls {
			result := ""
			t, ok := byName[tc.Function.Name]
			if !ok {
				result = fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
			} else if out, err := t.Run(ctx, tc.Function.Arguments); err != nil {
				result = fmt.Sprintf("error: tool %q failed: %v", tc.Function.Name, err)
			} else {
				result = out
			}
			msgs = append(msgs, model.Message{
				Role:       model.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}
	return nil, ErrMaxTurns
}
