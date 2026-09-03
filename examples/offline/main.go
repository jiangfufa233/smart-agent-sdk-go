// Command offline demonstrates the full agent loop without any network access
// by using the scripted fake models from the testutil package. It doubles as
// a smoke test for the SDK: schema generation, the tool-call loop, handoffs,
// skills and streaming.
//
// Usage:
//
//	go run ./examples/offline
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/example/agent-sdk/agent"
	"github.com/example/agent-sdk/handoff"
	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/skill"
	"github.com/example/agent-sdk/testutil"
	"github.com/example/agent-sdk/tool"
)

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
	fmt.Println("generated schema:", string(weatherTool.Spec().Function.Parameters))

	// 2. Runner loop: tool call -> tool result -> final answer.
	weatherModel := testutil.NewScripted(
		testutil.ToolCallStep("call_1", "get_weather", `{"city":"Beijing"}`),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != model.RoleTool || last.Content == "" {
				return nil, fmt.Errorf("expected tool result message, got %+v", last)
			}
			return testutil.TextStep("Sunny, 21C in Beijing (via " + last.Content + ").").Resp, nil
		}},
	)
	a := &agent.Agent{
		Name:         "weather-agent",
		Instructions: "You are a helpful assistant.",
		Model:        weatherModel,
		Tools:        []tool.Tool{weatherTool},
	}
	res, err := agent.NewRunner().Run(ctx, a, "What's the weather like in Beijing?")
	must(err)
	fmt.Println("agent>", res.Output)
	fmt.Println("conversation turns:", len(res.Messages))

	// 3. Handoff: wrap a sub-agent as a tool.
	researcherModel := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		in := req.Messages[len(req.Messages)-1].Content
		return testutil.TextStep("researcher handled: " + in).Resp, nil
	}})
	sub := &agent.Agent{Name: "researcher", Model: researcherModel}
	ht, err := handoff.AsTool(sub, nil)
	must(err)
	out, err := ht.Run(ctx, `{"message":"look this up"}`)
	must(err)
	fmt.Println("handoff result>", out)

	// 4. Skills: parse SKILL.md frontmatter and expose as a tool.
	dir, err := os.MkdirTemp("", "skilldemo")
	must(err)
	defer func() { _ = os.RemoveAll(dir) }()
	must(os.WriteFile(dir+"/SKILL.md", []byte(
		"---\nname: pdf-extract\ndescription: Extract text from PDF files\n---\n# Steps\n1. Parse the PDF\n2. Emit text\n"), 0o644))
	skills, err := skill.LoadDir(dir)
	must(err)
	st, err := skills[0].Tool()
	must(err)
	body, err := st.Run(ctx, "{}")
	must(err)
	fmt.Printf("skill %q loaded, body:\n%s\n", skills[0].Name, body)

	// 5. Streaming: incremental events via Runner.RunStream.
	streamModel := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("Hel"), testutil.TextChunk("lo"), testutil.TextChunk(", world!")},
		FinishReason: "stop",
		Usage:        model.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7},
	})
	run := agent.NewRunner().RunStream(ctx, &agent.Agent{Name: "streamer", Model: streamModel}, "Greet the world.")
	for ev := range run.Events {
		switch ev.Type {
		case agent.StreamTextDelta:
			fmt.Print(ev.Text)
		case agent.StreamFinalOutput:
			fmt.Printf("\nstream> done, finish=%s, usage=%d tokens\n", ev.FinishReason, ev.Usage.TotalTokens)
		case agent.StreamRunError:
			fmt.Println("stream> error:", ev.Err)
		}
	}
	if _, err := run.Result(); err != nil {
		must(err)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
