// Package handoff provides agent-to-agent delegation primitives.
//
// The MVP implements the "agent-as-tool" pattern: a target agent is wrapped
// as a tool, so a supervisor agent can delegate tasks to it through the
// regular tool-call loop.
package handoff

import (
	"context"
	"fmt"
	"strings"

	"github.com/jiangfufa233/openai-agent-sdk-go/agent"
	"github.com/jiangfufa233/openai-agent-sdk-go/tool"
)

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
