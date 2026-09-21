package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestDelegateRunsChild(t *testing.T) {
	sub := &agent.Agent{
		ID:   "research-agent",
		Name: "Research Agent",
		Model: testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			in := req.Messages[len(req.Messages)-1].Content
			return testutil.TextStep("handled: " + in).Resp, nil
		}}),
	}
	ht, err := Delegate(sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ht.Spec().Function.Name, "delegate_to_research_agent"; got != want {
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

func TestDelegateForwardsChildStream(t *testing.T) {
	lookup, err := tool.NewFunction("lookup", "look up a fact", func(context.Context, struct{}) (string, error) {
		return "fact", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	childModel := testutil.NewScriptedStream(
		testutil.StreamStep{Deltas: []model.StreamEvent{
			testutil.ReasoningChunk("researching"),
			testutil.ToolCallChunk(model.ToolCall{ID: "child_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: "lookup", Arguments: "{}"}}),
		}, FinishReason: "tool_calls"},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("child result")}, FinishReason: "stop"},
	)
	child := &agent.Agent{ID: "researcher", Name: "Researcher", Model: childModel, Tools: []tool.Tool{lookup}}
	delegated, err := Delegate(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	managerModel := testutil.NewScriptedStream(
		testutil.StreamStep{Deltas: []model.StreamEvent{
			testutil.ToolCallChunk(model.ToolCall{ID: "parent_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: delegated.Spec().Function.Name, Arguments: `{"message":"research this"}`}}),
		}, FinishReason: "tool_calls"},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("manager final")}, FinishReason: "stop"},
	)
	manager := &agent.Agent{ID: "manager", Name: "Manager", Model: managerModel, Tools: []tool.Tool{delegated}}

	run := agent.NewRunner().RunStream(context.Background(), manager, "investigate")
	var events []agent.StreamEvent
	for event := range run.Events {
		events = append(events, event)
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "manager final" {
		t.Fatalf("output = %q", res.Output)
	}

	var task agent.TaskRef
	var started, completed, childReasoning, childToolStarted, childToolArgs, childToolFinished, childTool, parentResult bool
	terminals := 0
	for _, event := range events {
		if event.Type == agent.StreamFinalOutput || event.Type == agent.StreamRunError {
			terminals++
		}
		switch event.Type {
		case agent.StreamTaskStarted:
			started = true
			task = event.Task
			if event.Agent != (agent.AgentRef{ID: "researcher", Name: "Researcher"}) {
				t.Fatalf("task start agent = %+v", event.Agent)
			}
		case agent.StreamTaskCompleted:
			completed = true
			if event.Task != task || event.Text != "child result" {
				t.Fatalf("task completion = %+v", event)
			}
		case agent.StreamReasoningDelta:
			if event.Agent.ID == "researcher" {
				childReasoning = event.Reasoning == "researching" && event.Task == task
			}
		case agent.StreamToolCallStarted:
			childToolStarted = event.Agent.ID == "researcher" && event.Call.ID == "child_call" && event.Task == task
		case agent.StreamToolCallArgs:
			childToolArgs = event.Agent.ID == "researcher" && event.Call.ID == "child_call" && event.Call.Function.Arguments == "{}" && event.Task == task
		case agent.StreamToolCallFinished:
			childToolFinished = event.Agent.ID == "researcher" && event.Call.ID == "child_call" && event.Call.Function.Arguments == "{}" && event.Task == task
		case agent.StreamToolResult:
			if event.Agent.ID == "researcher" && event.Call.Function.Name == "lookup" && event.Result == "fact" && event.Task == task {
				childTool = true
			}
			if event.Agent.ID == "manager" && event.Call.ID == "parent_call" && event.Result == "child result" {
				parentResult = true
			}
		case agent.StreamRunStarted:
			if event.Agent.ID == "researcher" {
				t.Fatal("child run_started leaked into root stream")
			}
		}
	}
	if !started || !completed || !childReasoning || !childToolStarted || !childToolArgs || !childToolFinished || !childTool || !parentResult || terminals != 1 {
		t.Fatalf("events did not preserve delegation contract: %+v", events)
	}
	if task.ID == "" || task.ParentID == "" || task.RootRunID == "" || task.SourceRunID == "" || task.RootRunID == task.SourceRunID {
		t.Fatalf("child task references = %+v", task)
	}
}

func TestDelegateChildFailureLetsManagerRecover(t *testing.T) {
	childErr := errors.New("research backend unavailable")
	child := &agent.Agent{ID: "failing-child", Name: "Failing Child", Model: model.ModelFunc(func(context.Context, *model.Request) (*model.Response, error) {
		return nil, childErr
	})}
	delegated, err := Delegate(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	managerModel := testutil.NewScriptedStream(
		testutil.StreamStep{Deltas: []model.StreamEvent{
			testutil.ToolCallChunk(model.ToolCall{ID: "delegate_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: delegated.Spec().Function.Name, Arguments: `{"message":"do research"}`}}),
		}, FinishReason: "tool_calls"},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("recovered")}, FinishReason: "stop"},
	)
	manager := &agent.Agent{ID: "manager", Name: "Manager", Model: managerModel, Tools: []tool.Tool{delegated}}
	run := agent.NewRunner().RunStream(context.Background(), manager, "investigate")
	var events []agent.StreamEvent
	for event := range run.Events {
		events = append(events, event)
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "recovered" || len(res.ToolErrors) != 1 || !errors.Is(res.ToolErrors[0], childErr) {
		t.Fatalf("result = %+v", res)
	}
	if req := managerModel.LastRequest(); len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != model.RoleTool || !strings.Contains(req.Messages[len(req.Messages)-1].Content, childErr.Error()) {
		t.Fatalf("manager did not receive delegated failure: %+v", req)
	}
	var failed, parentToolErr bool
	terminals := 0
	for _, event := range events {
		if event.Type == agent.StreamFinalOutput || event.Type == agent.StreamRunError {
			terminals++
		}
		if event.Type == agent.StreamTaskFailed && event.Agent.ID == "failing-child" && errors.Is(event.Err, childErr) {
			failed = true
		}
		if event.Type == agent.StreamToolResult && event.Agent.ID == "manager" && event.Call.ID == "delegate_call" && event.ToolErr != nil && errors.Is(event.ToolErr, childErr) {
			parentToolErr = true
		}
	}
	if !failed || !parentToolErr || terminals != 1 {
		t.Fatalf("delegation failure events = %+v", events)
	}
}

func TestDelegateStreamToolTimeout(t *testing.T) {
	child := &agent.Agent{
		ID:   "slow-child",
		Name: "Slow Child",
		Model: testutil.NewScriptedStream(testutil.StreamStep{
			Deltas:       []model.StreamEvent{testutil.TextChunk("late")},
			DeltaDelay:   time.Second,
			FinishReason: "stop",
		}),
	}
	delegated, err := Delegate(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := &agent.Agent{
		ID:   "manager",
		Name: "Manager",
		Model: testutil.NewScriptedStream(
			testutil.StreamStep{Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{ID: "delegate_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: delegated.Spec().Function.Name, Arguments: `{"message":"wait"}`}}),
			}, FinishReason: "tool_calls"},
			testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("recovered")}, FinishReason: "stop"},
		),
		Tools: []tool.Tool{delegated},
	}
	run := (&agent.Runner{ToolTimeout: 20 * time.Millisecond}).RunStream(context.Background(), manager, "investigate")
	var taskFailed, parentToolErr bool
	terminals := 0
	for event := range run.Events {
		if event.Type == agent.StreamFinalOutput || event.Type == agent.StreamRunError {
			terminals++
		}
		if event.Type == agent.StreamTaskFailed && event.Agent.ID == child.ID && errors.Is(event.Err, context.DeadlineExceeded) {
			taskFailed = true
		}
		if event.Type == agent.StreamToolResult && event.Agent.ID == manager.ID && event.Call.ID == "delegate_call" && event.ToolErr != nil && errors.Is(event.ToolErr, context.DeadlineExceeded) {
			parentToolErr = true
		}
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "recovered" || len(res.ToolErrors) != 1 || !errors.Is(res.ToolErrors[0], context.DeadlineExceeded) {
		t.Fatalf("result = %+v", res)
	}
	if !taskFailed || !parentToolErr || terminals != 1 {
		t.Fatalf("delegated timeout events were incomplete")
	}
}

func TestDelegateCancellationDrainsChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child := &agent.Agent{
		ID:   "blocked-child",
		Name: "Blocked Child",
		Model: testutil.NewScriptedStream(testutil.StreamStep{
			Deltas:       []model.StreamEvent{testutil.TextChunk("late")},
			DeltaDelay:   time.Second,
			FinishReason: "stop",
		}),
	}
	delegated, err := Delegate(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := &agent.Agent{
		ID:   "manager",
		Name: "Manager",
		Model: testutil.NewScriptedStream(testutil.StreamStep{Deltas: []model.StreamEvent{
			testutil.ToolCallChunk(model.ToolCall{ID: "delegate_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: delegated.Spec().Function.Name, Arguments: `{"message":"wait"}`}}),
		}, FinishReason: "tool_calls"}),
		Tools: []tool.Tool{delegated},
	}
	run := agent.NewRunner().RunStream(ctx, manager, "investigate")
	var started bool
	terminals := 0
	for event := range run.Events {
		if event.Type == agent.StreamTaskStarted && event.Agent.ID == child.ID {
			started = true
			cancel()
		}
		if event.Type == agent.StreamFinalOutput || event.Type == agent.StreamRunError {
			terminals++
		}
	}
	_, err = run.Result()
	if !started || !errors.Is(err, context.Canceled) || terminals != 1 {
		t.Fatalf("cancellation result: started=%v terminals=%d err=%v", started, terminals, err)
	}
}

func TestDelegateStreamHandlesSlowConsumer(t *testing.T) {
	const childDeltas = 384
	deltas := make([]model.StreamEvent, childDeltas)
	for i := range deltas {
		deltas[i] = testutil.TextChunk("x")
	}
	child := &agent.Agent{
		ID:    "verbose-child",
		Name:  "Verbose Child",
		Model: testutil.NewScriptedStream(testutil.StreamStep{Deltas: deltas, FinishReason: "stop"}),
	}
	delegated, err := Delegate(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := &agent.Agent{
		ID:   "manager",
		Name: "Manager",
		Model: testutil.NewScriptedStream(
			testutil.StreamStep{Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{ID: "delegate_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: delegated.Spec().Function.Name, Arguments: `{"message":"produce"}`}}),
			}, FinishReason: "tool_calls"},
			testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("complete")}, FinishReason: "stop"},
		),
		Tools: []tool.Tool{delegated},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := agent.NewRunner().RunStream(ctx, manager, "investigate")
	time.Sleep(20 * time.Millisecond)
	seen := 0
	for event := range run.Events {
		if event.Type == agent.StreamTextDelta && event.Agent.ID == child.ID {
			seen++
		}
		time.Sleep(time.Millisecond)
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "complete" || seen != childDeltas {
		t.Fatalf("output = %q, child deltas = %d", res.Output, seen)
	}
}

// newDelegationFixture builds a child with one tool and a manager that
// delegates to it, returning the delegation tool.
func newDelegationFixture() (*agent.Agent, tool.Tool) {
	lookup, err := tool.NewFunction("lookup", "look up a fact", func(context.Context, struct{}) (string, error) {
		return "fact", nil
	})
	if err != nil {
		panic(err)
	}
	child := &agent.Agent{
		ID:    "researcher",
		Name:  "Researcher",
		Model: testutil.NewScripted(testutil.ToolCallStep("child_call", "lookup", "{}"), testutil.TextStep("child result")),
		Tools: []tool.Tool{lookup},
	}
	delegated, err := Delegate(child, nil)
	if err != nil {
		panic(err)
	}
	return child, delegated
}

func TestPolicyWrappedDelegationDeniesWithoutRunningChild(t *testing.T) {
	_, delegated := newDelegationFixture()
	policyCalled := false
	denied := tool.WithPolicy(delegated, tool.PolicyFunc(func(_ context.Context, call tool.ToolCall) error {
		if call.Name != delegated.Spec().Function.Name || call.Arguments != `{"message":"research this"}` {
			t.Errorf("policy saw call %+v", call)
		}
		policyCalled = true
		return errors.New("delegation is off hours")
	}))
	manager := &agent.Agent{
		ID:    "manager",
		Name:  "Manager",
		Model: testutil.NewScripted(testutil.ToolCallStep("parent_call", denied.Spec().Function.Name, `{"message":"research this"}`), testutil.TextStep("manager final")),
		Tools: []tool.Tool{denied},
	}
	res, err := agent.NewRunner().Run(context.Background(), manager, "investigate")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "manager final" {
		t.Fatalf("output = %q", res.Output)
	}
	if !policyCalled {
		t.Fatal("policy was never consulted for the delegation call")
	}
	if len(res.ToolErrors) != 1 {
		t.Fatalf("tool errors = %d, want 1", len(res.ToolErrors))
	}
	var authErr *tool.AuthorizationError
	if !errors.As(res.ToolErrors[0].Err, &authErr) || !strings.Contains(authErr.Error(), "off hours") {
		t.Fatalf("tool error = %v, want policy denial", res.ToolErrors[0].Err)
	}
}

func TestPolicyWrappedDelegationKeepsStreamingForwarding(t *testing.T) {
	_, delegated := newDelegationFixture()
	guarded := tool.WithPolicy(delegated, tool.Allowlist(delegated.Spec().Function.Name))
	manager := &agent.Agent{
		ID:   "manager",
		Name: "Manager",
		Model: testutil.NewScriptedStream(
			testutil.StreamStep{Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{ID: "parent_call", Type: "function", Index: 0, Function: model.FunctionCall{Name: guarded.Spec().Function.Name, Arguments: `{"message":"research this"}`}}),
			}, FinishReason: "tool_calls"},
			testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("manager final")}, FinishReason: "stop"},
		),
		Tools: []tool.Tool{guarded},
	}

	run := agent.NewRunner().RunStream(context.Background(), manager, "investigate")
	var taskStarted bool
	for event := range run.Events {
		if event.Type == agent.StreamTaskStarted && event.Agent.ID == "researcher" {
			taskStarted = true
		}
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "manager final" || len(res.ToolErrors) != 0 {
		t.Fatalf("result = (%q, %d tool errors)", res.Output, len(res.ToolErrors))
	}
	if !taskStarted {
		t.Fatal("guarded delegation lost task-scoped event forwarding")
	}
}

func TestNewFirstClass(t *testing.T) {
	target := &agent.Agent{ID: "research-specialist", Name: "Research Specialist", Model: testutil.NewScripted(testutil.TextStep("ok"))}
	h := New(target)
	if h.Target != target {
		t.Fatal("New must return a handoff to the given target")
	}
	spec := h.Spec()
	if spec.Function.Name != "transfer_to_research_specialist" {
		t.Fatalf("tool name = %q", spec.Function.Name)
	}
	if string(spec.Function.Parameters) != `{"type":"object","properties":{}}` {
		t.Fatalf("parameters = %s", spec.Function.Parameters)
	}
}

func TestDelegateNilTarget(t *testing.T) {
	if _, err := Delegate(nil, nil); err == nil {
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
