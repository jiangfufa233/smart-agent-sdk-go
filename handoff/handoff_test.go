package handoff

import (
	"context"
	"strings"
	"testing"

	"github.com/jiangfufa233/openai-agent-sdk-go/agent"
	"github.com/jiangfufa233/openai-agent-sdk-go/model"
	"github.com/jiangfufa233/openai-agent-sdk-go/testutil"
)

func TestAsToolDelegates(t *testing.T) {
	sub := &agent.Agent{
		Name: "Research Agent",
		Model: testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			in := req.Messages[len(req.Messages)-1].Content
			return testutil.TextStep("handled: " + in).Resp, nil
		}}),
	}
	ht, err := AsTool(sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ht.Spec().Function.Name, "transfer_to_research_agent"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	if !strings.Contains(ht.Spec().Function.Description, "Research Agent") {
		t.Fatalf("description should name the target: %q", ht.Spec().Function.Description)
	}
	out, err := ht.Run(context.Background(), `{"message":"look this up"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled: look this up" {
		t.Fatalf("out = %q", out)
	}
}

func TestAsToolNilTarget(t *testing.T) {
	if _, err := AsTool(nil, nil); err == nil {
		t.Fatal("expected error for nil target")
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("Deep Research v2"); got != "deep_research_v2" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeName("   "); got != "agent" {
		t.Fatalf("empty name should fall back to agent, got %q", got)
	}
}
