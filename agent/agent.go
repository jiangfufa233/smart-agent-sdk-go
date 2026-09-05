// Package agent provides the Agent configuration type and the Runner that
// executes the model/tool-call loop.
package agent

import (
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// Agent is a configurable unit that answers instructions using a model and
// an optional set of tools.
type Agent struct {
	// Name identifies the agent (used in handoffs and logs).
	Name string
	// Instructions is the system prompt guiding the agent's behavior.
	Instructions string
	// Model is the chat provider used to generate responses.
	Model model.Model
	// ModelName is the provider-specific model identifier (e.g. "gpt-4o-mini").
	ModelName string
	// Tools are the tools the agent may call during a run.
	Tools []tool.Tool
	// Handoffs are first-class transfers to other agents. Each is exposed to
	// the model as a "transfer_to_<name>" tool; calling it continues the run
	// with the target agent in the same conversation.
	Handoffs []Handoff
	// Settings carries optional sampling parameters merged into every
	// request (temperature, tool_choice, response_format, ...).
	Settings *model.Settings
	// InputGuardrails run once per Run on the user input before the first
	// model call, concurrently; a tripwire fails the run without any model
	// call.
	InputGuardrails []InputGuardrail
	// OutputGuardrails run once on the final output, using the guardrails of
	// the agent that produced it; a tripwire fails the run before the result
	// is returned.
	OutputGuardrails []OutputGuardrail
}
