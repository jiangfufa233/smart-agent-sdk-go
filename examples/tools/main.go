// Command tools demonstrates an agent calling a function tool and turning
// the result into a final answer.
//
// Usage:
//
//	OPENAI_API_KEY=sk-... go run ./examples/tools
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jiangfufa233/openai-agent-sdk-go/agent"
	"github.com/jiangfufa233/openai-agent-sdk-go/model/openai"
	"github.com/jiangfufa233/openai-agent-sdk-go/tool"
)

type weatherArgs struct {
	City string `json:"city" desc:"City name to get the weather for"`
}

// getWeather returns deterministic fake data for the scaffold demo.
func getWeather(ctx context.Context, in weatherArgs) (string, error) {
	return fmt.Sprintf(`{"city":%q,"temperature_c":21,"condition":"sunny"}`, in.City), nil
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY is not set")
		os.Exit(1)
	}

	weatherTool, err := tool.NewFunction("get_weather", "Return the current weather for a city.", getWeather)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build tool:", err)
		os.Exit(1)
	}

	a := &agent.Agent{
		Name:         "weather-agent",
		Instructions: "You are a helpful assistant. Use tools when they can answer the question.",
		Model:        openai.New(openai.Config{APIKey: apiKey}),
		ModelName:    "gpt-4o-mini",
		Tools:        []tool.Tool{weatherTool},
	}

	res, err := agent.NewRunner().Run(context.Background(), a, "What's the weather like in Beijing today?")
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	fmt.Println("agent>", res.Output)
}
