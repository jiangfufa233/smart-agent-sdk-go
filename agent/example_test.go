package agent_test

import (
	"context"
	"fmt"
	"log"

	"github.com/jiangfufa233/openai-agent-sdk-go/agent"
	"github.com/jiangfufa233/openai-agent-sdk-go/model"
)

// This example runs a minimal agent against a scripted in-process model.
// For real usage, replace the ModelFunc with a provider adapter, e.g.
//
//	m := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
func Example() {
	m := model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		return &model.Response{
			Message:      model.Message{Role: model.RoleAssistant, Content: "Sunny, 21C in Beijing."},
			FinishReason: "stop",
		}, nil
	})

	res, err := agent.NewRunner().Run(context.Background(), &agent.Agent{
		Name:         "demo",
		Instructions: "You are a weather assistant.",
		Model:        m,
	}, "What's the weather in Beijing?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Output)
	// Output: Sunny, 21C in Beijing.
}
