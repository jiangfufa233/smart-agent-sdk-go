// Command offline demonstrates the full agent loop without any network access
// by using a scripted fake model. It doubles as a smoke test for the SDK:
// schema generation, the tool-call loop, handoffs and skills.
//
// Usage:
//
//	go run ./examples/offline
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/example/agent-sdk/agent"
	"github.com/example/agent-sdk/handoff"
	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/skill"
	"github.com/example/agent-sdk/tool"
)

// scriptedModel returns a tool call on the first turn, then a final answer.
type scriptedModel struct{ calls int }

func (f *scriptedModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	f.calls++
	if f.calls == 1 {
		return &model.Response{Message: model.Message{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: model.FunctionCall{Name: "get_weather", Arguments: `{"city":"Beijing"}`},
			}},
		}, FinishReason: "tool_calls"}, nil
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != model.RoleTool || last.Content == "" {
		return nil, fmt.Errorf("expected tool result message, got %+v", last)
	}
	return &model.Response{Message: model.Message{
		Role:    model.RoleAssistant,
		Content: "Sunny, 21C in Beijing (via " + last.Content + ").",
	}, FinishReason: "stop"}, nil
}

// echoModel answers directly, used by the handoff sub-agent.
type echoModel struct{}

func (e *echoModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	in := req.Messages[len(req.Messages)-1].Content
	return &model.Response{Message: model.Message{
		Role:    model.RoleAssistant,
		Content: "researcher handled: " + in,
	}, FinishReason: "stop"}, nil
}

type weatherArgs struct {
	City string `json:"city" desc:"City name to get the weather for"`
	Days int    `json:"days,omitempty" desc:"Forecast horizon in days"`
}

func main() {
	ctx := context.Background()

	// 1. Reflection-based JSON schema generation.
	weatherTool, err := tool.NewFunction("get_weather", "Return the current weather for a city.",
		func(ctx context.Context, in weatherArgs) (string, error) {
			return fmt.Sprintf(`{"city":%q,"temperature_c":21}`, in.City), nil
		})
	must(err)
	var schema map[string]any
	must(json.Unmarshal(weatherTool.Spec().Function.Parameters, &schema))
	fmt.Println("generated schema:", string(weatherTool.Spec().Function.Parameters))

	// 2. Runner loop: tool call -> tool result -> final answer.
	a := &agent.Agent{
		Name:         "weather-agent",
		Instructions: "You are a helpful assistant.",
		Model:        &scriptedModel{},
		Tools:        []tool.Tool{weatherTool},
	}
	res, err := agent.NewRunner().Run(ctx, a, "What's the weather like in Beijing?")
	must(err)
	fmt.Println("agent>", res.Output)
	fmt.Println("conversation turns:", len(res.Messages))

	// 3. Handoff: wrap a sub-agent as a tool.
	sub := &agent.Agent{Name: "researcher", Model: &echoModel{}}
	ht, err := handoff.AsTool(sub, nil)
	must(err)
	out, err := ht.Run(ctx, `{"message":"look this up"}`)
	must(err)
	fmt.Println("handoff result>", out)

	// 4. Skills: parse SKILL.md frontmatter and expose as a tool.
	dir, err := os.MkdirTemp("", "skilldemo")
	must(err)
	defer os.RemoveAll(dir)
	must(os.WriteFile(dir+"/SKILL.md", []byte(
		"---\nname: pdf-extract\ndescription: Extract text from PDF files\n---\n# Steps\n1. Parse the PDF\n2. Emit text\n"), 0o644))
	skills, err := skill.LoadDir(dir)
	must(err)
	st, err := skills[0].Tool()
	must(err)
	body, err := st.Run(ctx, "{}")
	must(err)
	fmt.Printf("skill %q loaded, body:\n%s\n", skills[0].Name, body)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
