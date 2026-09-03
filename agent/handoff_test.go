package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

func handoffAgent(name, sys string, m model.Model, handoffs ...Handoff) *Agent {
	return &Agent{Name: name, Instructions: sys, Model: m, Handoffs: handoffs}
}

func TestHandoffSlug(t *testing.T) {
	cases := map[string]string{
		"Research Specialist": "research_specialist",
		"B-One":               "b_one",
		"  MiXeD 9 Case! ":    "mixed_9_case",
		"///":                 "agent",
		"":                    "agent",
	}
	for in, want := range cases {
		if got := handoffSlug(in); got != want {
			t.Errorf("handoffSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandoffSpec(t *testing.T) {
	spec := Handoff{Target: &Agent{Name: "Research Specialist"}}.Spec()
	if spec.Type != "function" || spec.Function.Name != "transfer_to_research_specialist" {
		t.Fatalf("spec = %+v", spec)
	}
	if !strings.Contains(spec.Function.Description, "Research Specialist") {
		t.Fatalf("description = %q", spec.Function.Description)
	}
	if string(spec.Function.Parameters) != `{"type":"object","properties":{}}` {
		t.Fatalf("parameters = %s", spec.Function.Parameters)
	}
	h := Handoff{Target: &Agent{Name: "b"}, Name: "escalate", Description: "custom"}
	if got := h.Spec().Function.Name; got != "escalate" {
		t.Fatalf("override name = %q", got)
	}
	if got := h.Spec().Function.Description; got != "custom" {
		t.Fatalf("override description = %q", got)
	}
}

func TestHandoffTransfersRun(t *testing.T) {
	specialistModel := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		// The target continues the same transcript with its own system
		// prompt and without the handoff tool.
		msgs := req.Messages
		if len(msgs) != 4 {
			return nil, fmt.Errorf("history = %d messages, want 4", len(msgs))
		}
		if msgs[0].Role != model.RoleSystem || msgs[0].Content != "specialist sys" {
			return nil, fmt.Errorf("system prompt not switched: %+v", msgs[0])
		}
		if msgs[1].Content != "please" {
			return nil, fmt.Errorf("user message lost: %+v", msgs[1])
		}
		last := msgs[3]
		if last.Role != model.RoleTool || last.Content != `{"handoff_to":"specialist"}` {
			return nil, fmt.Errorf("handoff marker missing: %+v", last)
		}
		if len(req.Tools) != 0 {
			return nil, fmt.Errorf("target must not see the handoff tool: %+v", req.Tools)
		}
		return testutil.TextStep("done by specialist").Resp, nil
	}})
	a := handoffAgent("triage", "triage sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_specialist", "{}"),
	), Handoff{Target: handoffAgent("specialist", "specialist sys", specialistModel)})

	res, err := NewRunner().Run(context.Background(), a, "please")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "done by specialist" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Agent != a.Handoffs[0].Target {
		t.Fatalf("final agent = %v", res.Agent.Name)
	}
	if len(res.Transfers) != 1 || res.Transfers[0] != "specialist" {
		t.Fatalf("transfers = %v", res.Transfers)
	}
	// sysB, user, assistant(toolcall), tool result, assistant final.
	if len(res.Messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(res.Messages))
	}
	if res.Messages[0].Content != "specialist sys" {
		t.Fatalf("transcript system prompt = %q", res.Messages[0].Content)
	}
}

func TestHandoffChain(t *testing.T) {
	c := handoffAgent("c", "c sys", testutil.NewScripted(testutil.TextStep("c done")))
	b := handoffAgent("b", "b sys", testutil.NewScripted(
		testutil.ToolCallStep("h2", "transfer_to_c", "{}"),
	), Handoff{Target: c})
	a := handoffAgent("a", "a sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_b", "{}"),
	), Handoff{Target: b})

	res, err := NewRunner().Run(context.Background(), a, "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "c done" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Agent != c {
		t.Fatalf("final agent = %q", res.Agent.Name)
	}
	if strings.Join(res.Transfers, ",") != "b,c" {
		t.Fatalf("transfers = %v", res.Transfers)
	}
}

func TestHandoffAlongsideRegularTools(t *testing.T) {
	specialistModel := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		msgs := req.Messages
		// sysB, user, assistant(2 calls), tool(weather), tool(handoff).
		if len(msgs) != 5 {
			return nil, fmt.Errorf("history = %d messages, want 5", len(msgs))
		}
		if msgs[3].Role != model.RoleTool || !strings.Contains(msgs[3].Content, "21") {
			return nil, fmt.Errorf("weather result missing: %+v", msgs[3])
		}
		if msgs[4].Content != `{"handoff_to":"b"}` {
			return nil, fmt.Errorf("handoff marker missing: %+v", msgs[4])
		}
		return testutil.TextStep("specialist done").Resp, nil
	}})
	b := handoffAgent("b", "b sys", specialistModel)
	a := handoffAgent("a", "a sys", testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		return &model.Response{
			Message: model.Message{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{
					{ID: "t1", Type: "function", Function: model.FunctionCall{Name: "get_weather", Arguments: "{}"}},
					{ID: "t2", Type: "function", Function: model.FunctionCall{Name: "transfer_to_b", Arguments: "{}"}},
				},
			},
			FinishReason: "tool_calls",
		}, nil
	}}), Handoff{Target: b})
	a.Tools = []tool.Tool{weatherTool()}

	res, err := NewRunner().Run(context.Background(), a, "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "specialist done" || res.Agent != b {
		t.Fatalf("res = %+v", res)
	}
}

func TestHandoffValidation(t *testing.T) {
	cases := []struct {
		name string
		a    *Agent
		want string
	}{
		{"nil target", handoffAgent("a", "", testutil.NewScripted(testutil.TextStep("x")),
			Handoff{}), "has no target"},
		{"target without model", handoffAgent("a", "", testutil.NewScripted(testutil.TextStep("x")),
			Handoff{Target: &Agent{Name: "b"}}), "no model configured"},
		{"conflicts with regular tool", func() *Agent {
			a := handoffAgent("a", "", testutil.NewScripted(
				testutil.ToolCallStep("h1", "transfer_to_b", "{}")),
				Handoff{Target: handoffAgent("b", "", testutil.NewScripted(testutil.TextStep("x")))})
			a.Tools = []tool.Tool{failingTool("transfer_to_b", nil)}
			return a
		}(), "conflicts with a regular tool"},
		{"duplicate handoff", handoffAgent("a", "", testutil.NewScripted(testutil.TextStep("x")),
			Handoff{Target: handoffAgent("b", "", testutil.NewScripted(testutil.TextStep("x")))},
			Handoff{Target: handoffAgent("b", "", testutil.NewScripted(testutil.TextStep("x")))}),
			"duplicate handoff tool"},
		{"invalid switch target", handoffAgent("a", "", testutil.NewScripted(
			testutil.ToolCallStep("h1", "transfer_to_b", "{}")),
			Handoff{Target: handoffAgent("b", "b sys", testutil.NewScripted(
				testutil.ToolCallStep("h2", "transfer_to_c", "{}")),
				Handoff{Target: &Agent{Name: "c"}})}),
			`switch to agent "b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRunner().Run(context.Background(), tc.a, "x")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandoffMaxTurnsShared(t *testing.T) {
	calls := 0
	m := model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		calls++
		return testutil.ToolCallStep(fmt.Sprintf("c%d", calls), "get_weather", "{}").Resp, nil
	})
	b := &Agent{Name: "b", Instructions: "b sys", Model: m}
	a := handoffAgent("a", "a sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_b", "{}"),
	), Handoff{Target: b})
	a.Tools = []tool.Tool{weatherTool()}
	b.Tools = []tool.Tool{weatherTool()}

	// Turn 0: triage emits the handoff; turns 1..2: specialist loops.
	r := &Runner{MaxTurns: 3}
	_, err := r.Run(context.Background(), a, "x")
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("expected ErrMaxTurns, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

type handoffRecorder struct {
	*recorder
	handoffs []string
}

func (h *handoffRecorder) OnHandoff(ctx context.Context, from, to *Agent, runID string) {
	h.handoffs = append(h.handoffs, from.Name+"->"+to.Name)
}

func TestHandoffHookSequence(t *testing.T) {
	rec := &handoffRecorder{recorder: &recorder{}}
	b := handoffAgent("specialist", "b sys", testutil.NewScripted(testutil.TextStep("done")))
	a := handoffAgent("triage", "a sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_specialist", "{}"),
	), Handoff{Target: b})
	r := &Runner{Hooks: rec}
	if _, err := r.Run(context.Background(), a, "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run_start",
		"llm_call:0", "llm_response",
		"tool_call:transfer_to_specialist", "tool_result",
		"llm_call:1", "llm_response",
		"run_end",
	}
	if strings.Join(rec.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
	if strings.Join(rec.handoffs, ",") != "triage->specialist" {
		t.Fatalf("handoffs = %v", rec.handoffs)
	}
}

func TestPlainHooksIgnoreHandoffs(t *testing.T) {
	rec := &recorder{}
	b := handoffAgent("specialist", "b sys", testutil.NewScripted(testutil.TextStep("done")))
	a := handoffAgent("triage", "a sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_specialist", "{}"),
	), Handoff{Target: b})
	if _, err := (&Runner{Hooks: rec}).Run(context.Background(), a, "x"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(rec.events, ","), "handoff") {
		t.Fatalf("plain hooks saw handoff events: %v", rec.events)
	}
}

func TestSlogHooksHandoff(t *testing.T) {
	var buf bytes.Buffer
	h := SlogHooks(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	hh, ok := h.(HandoffHook)
	if !ok {
		t.Fatal("SlogHooks does not implement HandoffHook")
	}
	hh.OnHandoff(context.Background(), &Agent{Name: "triage"}, &Agent{Name: "specialist"}, "run1")
	if !strings.Contains(buf.String(), "from=triage") || !strings.Contains(buf.String(), "to=specialist") {
		t.Fatalf("log = %s", buf.String())
	}
}

func TestStreamHandoff(t *testing.T) {
	b := handoffAgent("specialist", "b sys", testutil.NewScripted(testutil.TextStep("done by b")))
	a := handoffAgent("triage", "a sys", testutil.NewScriptedStream(testutil.StreamStep{
		Deltas: []model.StreamEvent{testutil.ToolCallChunk(model.ToolCall{
			Index:    0,
			ID:       "h1",
			Type:     "function",
			Function: model.FunctionCall{Name: "transfer_to_specialist", Arguments: "{}"},
		})},
		FinishReason: "tool_calls",
	}), Handoff{Target: b})

	run := NewRunner().RunStream(context.Background(), a, "x")
	var types []StreamEventType
	var handoffEv *StreamEvent
	var toolResults int
	for ev := range run.Events {
		types = append(types, ev.Type)
		if ev.Type == StreamHandoff {
			cp := ev
			handoffEv = &cp
		}
		if ev.Type == StreamToolResult {
			toolResults++
			if ev.Result != `{"handoff_to":"specialist"}` {
				t.Fatalf("handoff tool result = %q", ev.Result)
			}
		}
	}
	want := []StreamEventType{
		StreamRunStarted,
		StreamToolCallStarted, StreamToolCallArgs, StreamToolCallFinished,
		StreamToolResult, StreamHandoff,
		StreamTextDelta, StreamFinalOutput,
	}
	if strings.Join(toStrings(types), ",") != strings.Join(toStrings(want), ",") {
		t.Fatalf("events = %v, want %v", types, want)
	}
	if handoffEv == nil || handoffEv.FromAgent != "triage" || handoffEv.ToAgent != "specialist" {
		t.Fatalf("handoff event = %+v", handoffEv)
	}
	if toolResults != 1 {
		t.Fatalf("tool result events = %d, want 1", toolResults)
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Agent != b || strings.Join(res.Transfers, ",") != "specialist" {
		t.Fatalf("result = %+v", res)
	}
}

func toStrings(types []StreamEventType) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return out
}
