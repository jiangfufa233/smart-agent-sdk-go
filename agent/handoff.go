package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// Handoff declares a first-class transfer of the conversation to another
// agent. It is exposed to the model as a "transfer_to_<name>" tool with no
// parameters: when the model calls it, the run continues with Target, which
// receives the full conversation history. Unlike the agent-as-tool pattern
// (see the handoff package), the transfer does not nest runs — there is one
// conversation and one token budget.
type Handoff struct {
	// Target is the agent that continues the conversation.
	Target *Agent
	// Name optionally overrides the generated tool name
	// ("transfer_to_<target slug>").
	Name string
	// Description optionally overrides the generated tool description.
	Description string
}

// Spec returns the tool definition exposed to the model for this handoff.
func (h Handoff) Spec() model.ToolParam {
	desc := h.Description
	if desc == "" {
		desc = fmt.Sprintf("Transfer the conversation to the %q agent; it continues handling the user's request from here.", h.Target.name())
	}
	return model.ToolParam{
		Type: "function",
		Function: model.FunctionDef{
			Name:        h.toolName(),
			Description: desc,
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func (h Handoff) toolName() string {
	if h.Name != "" {
		return h.Name
	}
	return "transfer_to_" + handoffSlug(h.Target.name())
}

// name is the display name used in logs, hooks and handoff markers; it never
// comes back empty.
func (a *Agent) name() string {
	if strings.TrimSpace(a.Name) == "" {
		return "agent"
	}
	return a.Name
}

// handoffSlug maps an agent name onto a tool-name-safe identifier.
func handoffSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if s := strings.Trim(b.String(), "_"); s != "" {
		return s
	}
	return "agent"
}

// agentRegs is the compiled tool/handoff surface of one agent for the
// duration of its turn in the run loop.
type agentRegs struct {
	specs    []model.ToolParam
	tools    map[string]tool.Tool
	handoffs map[string]Handoff
}

// buildAgentRegs compiles a's tools and handoffs into lookup tables. A
// handoff whose tool name clashes with a regular tool or another handoff is
// rejected; duplicate regular tools keep the existing last-wins behavior.
func buildAgentRegs(a *Agent) (*agentRegs, error) {
	regs := &agentRegs{
		specs:    make([]model.ToolParam, 0, len(a.Tools)+len(a.Handoffs)),
		tools:    make(map[string]tool.Tool, len(a.Tools)),
		handoffs: make(map[string]Handoff, len(a.Handoffs)),
	}
	for _, t := range a.Tools {
		spec := t.Spec()
		regs.specs = append(regs.specs, spec)
		regs.tools[spec.Function.Name] = t
	}
	for i, h := range a.Handoffs {
		switch {
		case h.Target == nil:
			return nil, fmt.Errorf("agent: handoff[%d] of agent %q has no target", i, a.name())
		case h.Target.Model == nil:
			return nil, fmt.Errorf("agent: handoff target %q has no model configured", h.Target.name())
		}
		name := h.toolName()
		if _, clash := regs.tools[name]; clash {
			return nil, fmt.Errorf("agent: handoff tool %q of agent %q conflicts with a regular tool", name, a.name())
		}
		if _, clash := regs.handoffs[name]; clash {
			return nil, fmt.Errorf("agent: duplicate handoff tool %q in agent %q", name, a.name())
		}
		regs.specs = append(regs.specs, h.Spec())
		regs.handoffs[name] = h
	}
	return regs, nil
}

// settingsFor snapshots the sampling settings of a.
func settingsFor(a *Agent) model.Settings {
	var s model.Settings
	if a.Settings != nil {
		s = *a.Settings
	}
	return s
}

// handoffToolOutput is the tool result recorded for a handoff call. It marks
// the transfer point in the transcript the next agent continues from.
func handoffToolOutput(h Handoff) string {
	b, _ := json.Marshal(struct {
		HandoffTo string `json:"handoff_to"`
	}{h.Target.name()})
	return string(b)
}

// switchSystemMessage replaces the leading system prompt in msgs with the
// instructions of target, keeping the rest of the conversation intact.
func switchSystemMessage(msgs []model.Message, target *Agent) []model.Message {
	i := 0
	for i < len(msgs) && msgs[i].Role == model.RoleSystem {
		i++
	}
	out := make([]model.Message, 0, len(msgs)+1)
	if target.Instructions != "" {
		out = append(out, model.Message{Role: model.RoleSystem, Content: target.Instructions})
	}
	return append(out, msgs[i:]...)
}
