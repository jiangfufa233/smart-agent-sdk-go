// Package handoff provides agent-to-agent delegation primitives.
//
// Two patterns are supported. First-class handoffs (agent.Handoff, built
// with New) transfer the conversation to the target agent inside the same
// run: the target sees the full history and the shared token budget. The
// "agent-as-tool" pattern (AsTool) nests an independent run instead — the
// supervisor receives only the target's final answer.
package handoff

import (
	"context"
	"fmt"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// New returns a first-class Handoff to target. Assign it to an Agent's
// Handoffs field to expose a "transfer_to_<name>" tool that continues the
// run with target in the same conversation.
func New(target *agent.Agent) agent.Handoff {
	return agent.Handoff{Target: target}
}

type handoffArgs struct {
	Message string `json:"message" desc:"The task or question to delegate to the target agent"`
}

// AsTool wraps target so it can be called as a tool by another agent.
// If r is nil a default Runner is used.
func AsTool(target *agent.Agent, r *agent.Runner) (tool.Tool, error) {
	if target == nil {
		return nil, fmt.Errorf("handoff: target agent is nil")
	}
	if r == nil {
		r = agent.NewRunner()
	}
	return tool.NewFunction(
		"transfer_to_"+sanitizeName(target.Name),
		fmt.Sprintf("Delegate a task to the %q agent.", target.Name),
		func(ctx context.Context, in handoffArgs) (string, error) {
			res, err := r.Run(ctx, target, in.Message)
			if err != nil {
				return "", err
			}
			return res.Output, nil
		},
	)
}

func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	if name == "" {
		name = "agent"
	}
	return name
}
