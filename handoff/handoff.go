// Package handoff provides agent-to-agent control-transfer and delegation
// primitives.
package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
)

// New returns a first-class Handoff to target. Assign it to an Agent's
// Handoffs field to expose a "transfer_to_<name>" tool that continues the
// current conversation with target.
func New(target *agent.Agent) agent.Handoff {
	return agent.Handoff{Target: target}
}

type handoffArgs struct {
	Message string `json:"message" desc:"The task or question to delegate to the target agent"`
}

// Delegate returns a tool that executes target as an isolated child task. The
// manager keeps control after the task completes; its output becomes the tool
// result. During a streaming parent run, the Runner forwards the child's
// process events into the root stream under a dedicated task reference.
func Delegate(target *agent.Agent, r *agent.Runner) (tool.Tool, error) {
	if target == nil {
		return nil, fmt.Errorf("handoff: target agent is nil")
	}
	if r == nil {
		r = agent.NewRunner()
	}
	return &delegatedTool{
		target: target,
		runner: r,
		spec: model.ToolParam{
			Type: "function",
			Function: model.FunctionDef{
				Name:        "delegate_to_" + sanitizeName(target.Name),
				Description: fmt.Sprintf("Delegate an isolated task to the %q agent and return its result.", target.Name),
				Parameters:  json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
			},
		},
	}, nil
}

type delegatedTool struct {
	target *agent.Agent
	runner *agent.Runner
	spec   model.ToolParam
}

func (d *delegatedTool) Spec() model.ToolParam { return d.spec }

func (d *delegatedTool) Run(ctx context.Context, arguments string) (string, error) {
	input, err := d.DelegationInput(arguments)
	if err != nil {
		return "", err
	}
	res, err := d.runner.Run(ctx, d.target, input)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

func (d *delegatedTool) DelegationTarget() *agent.Agent { return d.target }

func (d *delegatedTool) DelegationRunner() *agent.Runner { return d.runner }

func (d *delegatedTool) DelegationInput(arguments string) (string, error) {
	if arguments == "" {
		arguments = "{}"
	}
	var input handoffArgs
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "", fmt.Errorf("handoff: invalid delegation arguments: %w", err)
	}
	return input.Message, nil
}

func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	if name == "" {
		name = "agent"
	}
	return name
}
