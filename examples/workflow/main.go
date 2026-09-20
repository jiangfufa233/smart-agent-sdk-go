package main

import (
	"context"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/workflow"
)

func main() {
	goResearcher := &agent.Agent{ID: "go-researcher", Name: "Go Researcher", Model: testutil.NewScripted(testutil.TextStep("Go supports parametric polymorphism."))}
	rustResearcher := &agent.Agent{ID: "rust-researcher", Name: "Rust Researcher", Model: testutil.NewScripted(testutil.TextStep("Rust uses traits and monomorphization."))}
	synthesizer := &agent.Agent{ID: "synthesizer", Name: "Synthesizer", Model: model.ModelFunc(func(_ context.Context, req *model.Request) (*model.Response, error) {
		return &model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "Both languages use compile-time generic abstractions."}, FinishReason: "stop"}, nil
	})}

	plan := workflow.Plan{
		MaxParallel: 2,
		Tasks: []workflow.Task{
			{ID: "go", Agent: goResearcher, Input: "Summarize Go generics."},
			{ID: "rust", Agent: rustResearcher, Input: "Summarize Rust generics."},
			{
				ID:        "synthesize",
				Agent:     synthesizer,
				DependsOn: []string{"go", "rust"},
				BuildInput: func(_ context.Context, results workflow.Results) (string, error) {
					return results["go"].Result.Output + "\n" + results["rust"].Result.Output, nil
				},
			},
		},
	}

	run := workflow.NewRunner(nil).Run(context.Background(), plan)
	for event := range run.Events {
		fmt.Printf("%s task=%s agent=%s\n", event.Type, event.TaskID, event.Agent.Name)
	}
	res, err := run.Result()
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Tasks["synthesize"].Result.Output)
}
