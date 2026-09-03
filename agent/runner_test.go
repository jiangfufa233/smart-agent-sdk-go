package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jiangfufa233/openai-agent-sdk-go/model"
	"github.com/jiangfufa233/openai-agent-sdk-go/testutil"
	"github.com/jiangfufa233/openai-agent-sdk-go/tool"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// recorder captures hook events in order.
type recorder struct{ events []string }

func (r *recorder) OnRunStart(ctx context.Context, a *Agent, runID, input string) context.Context {
	r.events = append(r.events, "run_start")
	return ctx
}
func (r *recorder) OnRunEnd(ctx context.Context, a *Agent, runID string, res *RunResult, err error) {
	r.events = append(r.events, "run_end")
}
func (r *recorder) OnLLMCall(ctx context.Context, a *Agent, runID string, turn int, req *model.Request) {
	r.events = append(r.events, fmt.Sprintf("llm_call:%d", turn))
}
func (r *recorder) OnLLMResponse(ctx context.Context, a *Agent, runID string, turn int, resp *model.Response, err error, elapsed time.Duration) {
	r.events = append(r.events, "llm_response")
}
func (r *recorder) OnToolCall(ctx context.Context, a *Agent, runID string, name, args string) {
	r.events = append(r.events, "tool_call:"+name)
}
func (r *recorder) OnToolResult(ctx context.Context, a *Agent, runID string, name, result string, err error, elapsed time.Duration) {
	r.events = append(r.events, "tool_result")
}

type cityArgs struct {
	City string `json:"city"`
}

func weatherTool() tool.Tool {
	t, _ := tool.NewFunction("get_weather", "weather",
		func(ctx context.Context, in cityArgs) (string, error) {
			return `{"temp_c":21}`, nil
		})
	return t
}

func failingTool(name string, err error) tool.Tool {
	t, _ := tool.NewFunction(name, "always fails",
		func(ctx context.Context, in struct{}) (string, error) { return "", err })
	return t
}

func newAgent(m model.Model, tools ...tool.Tool) *Agent {
	return &Agent{Name: "test", Instructions: "sys", Model: m, Tools: tools}
}

func TestHappyLoop(t *testing.T) {
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "get_weather", `{"city":"SF"}`),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != model.RoleTool || !strings.Contains(last.Content, "21") {
				return nil, fmt.Errorf("unexpected tool message: %+v", last)
			}
			return &model.Response{
				Message:      model.Message{Role: model.RoleAssistant, Content: "21C"},
				FinishReason: "stop",
			}, nil
		}},
	)
	res, err := NewRunner().Run(context.Background(), newAgent(m, weatherTool()), "weather?")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "21C" {
		t.Fatalf("output = %q", res.Output)
	}
	// system, user, assistant(toolcall), tool, assistant(final)
	if len(res.Messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(res.Messages))
	}
	if res.RunID == "" || res.Duration <= 0 {
		t.Fatalf("run metadata missing: %+v", res)
	}
}

func TestUnknownToolFeedback(t *testing.T) {
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "nope", "{}"),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if !strings.Contains(last.Content, "unknown tool") {
				return nil, fmt.Errorf("model did not receive error text: %+v", last)
			}
			return &model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "ok"}}, nil
		}},
	)
	res, err := NewRunner().Run(context.Background(), newAgent(m), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolErrors) != 1 || res.ToolErrors[0].Tool != "nope" {
		t.Fatalf("tool errors = %+v", res.ToolErrors)
	}
}

func TestToolErrorRecorded(t *testing.T) {
	sentinel := errors.New("db down")
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "failing", "{}"),
		testutil.TextStep("done"),
	)
	res, err := NewRunner().Run(context.Background(), newAgent(m, failingTool("failing", sentinel)), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolErrors) != 1 {
		t.Fatalf("tool errors = %+v", res.ToolErrors)
	}
	if !errors.Is(res.ToolErrors[0], sentinel) {
		t.Fatalf("sentinel lost: %v", res.ToolErrors[0])
	}
	toolMsg := res.Messages[3]
	if !strings.Contains(toolMsg.Content, "db down") {
		t.Fatalf("error text not fed back: %+v", toolMsg)
	}
}

func TestToolPanicRecovered(t *testing.T) {
	t0, err := tool.NewFunction("bomb", "panics",
		func(ctx context.Context, in struct{}) (string, error) { panic("boom") })
	if err != nil {
		t.Fatal(err)
	}
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "bomb", "{}"),
		testutil.TextStep("survived"),
	)
	res, err := NewRunner().Run(context.Background(), newAgent(m, t0), "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "survived" {
		t.Fatalf("run should survive a panicking tool: %v", err)
	}
	if len(res.ToolErrors) != 1 || !strings.Contains(res.ToolErrors[0].Err.Error(), "panic: boom") {
		t.Fatalf("tool errors = %+v", res.ToolErrors)
	}
}

func TestToolTimeout(t *testing.T) {
	slow, err := tool.NewFunction("slow", "slow",
		func(ctx context.Context, in struct{}) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
				return "late", nil
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "slow", "{}"),
		testutil.TextStep("done"),
	)
	r := &Runner{ToolTimeout: 30 * time.Millisecond}
	res, err := r.Run(context.Background(), newAgent(m, slow), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolErrors) != 1 || !errors.Is(res.ToolErrors[0], context.DeadlineExceeded) {
		t.Fatalf("tool errors = %+v", res.ToolErrors)
	}
}

func TestOutputTruncation(t *testing.T) {
	big, err := tool.NewFunction("big", "big",
		func(ctx context.Context, in struct{}) (string, error) {
			return strings.Repeat("a", 100), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "big", "{}"),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if !strings.Contains(last.Content, "[output truncated") {
				return nil, fmt.Errorf("output not truncated: %q", last.Content)
			}
			if len(last.Content) > 100 {
				return nil, fmt.Errorf("truncated content too long: %d", len(last.Content))
			}
			return &model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "ok"}}, nil
		}},
	)
	r := &Runner{MaxToolOutputBytes: 16}
	if _, err := r.Run(context.Background(), newAgent(m, big), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestMaxTurnsTypedError(t *testing.T) {
	calls := 0
	m := model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		calls++
		return testutil.ToolCallStep("c1", "get_weather", "{}").Resp, nil
	})
	r := &Runner{MaxTurns: 3}
	_, err := r.Run(context.Background(), newAgent(m, weatherTool()), "x")
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("expected ErrMaxTurns via errors.Is, got %v", err)
	}
	var mte *MaxTurnsError
	if !errors.As(err, &mte) || mte.MaxTurns != 3 {
		t.Fatalf("expected *MaxTurnsError(3), got %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestSettingsPassthrough(t *testing.T) {
	temp := 0.7
	m := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		if req.Temperature == nil || *req.Temperature != 0.7 {
			return nil, fmt.Errorf("temperature not passed: %+v", req.Settings)
		}
		return testutil.TextStep("ok").Resp, nil
	}})
	a := newAgent(m)
	a.Settings = &model.Settings{Temperature: &temp}
	res, err := NewRunner().Run(context.Background(), a, "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "ok" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestHooksSequence(t *testing.T) {
	rec := &recorder{}
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "get_weather", `{"city":"SF"}`),
		testutil.TextStep("done"),
	)
	r := &Runner{Hooks: rec}
	if _, err := r.Run(context.Background(), newAgent(m, weatherTool()), "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run_start",
		"llm_call:0", "llm_response",
		"tool_call:get_weather", "tool_result",
		"llm_call:1", "llm_response",
		"run_end",
	}
	if strings.Join(rec.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
}

func TestNoModelConfigured(t *testing.T) {
	_, err := NewRunner().Run(context.Background(), &Agent{Name: "x"}, "x")
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("expected no-model error, got %v", err)
	}
}

func TestRunWithHistoryContinuation(t *testing.T) {
	first := testutil.NewScripted(testutil.TextStep("one"))
	res1, err := NewRunner().Run(context.Background(), newAgent(first), "hi")
	if err != nil {
		t.Fatal(err)
	}

	second := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		if len(req.Messages) != 4 { // system, user, assistant, user
			return nil, fmt.Errorf("history not carried over: %d messages", len(req.Messages))
		}
		if req.Messages[0].Role != model.RoleSystem || req.Messages[0].Content != "sys" {
			return nil, fmt.Errorf("system prompt lost: %+v", req.Messages[0])
		}
		return testutil.TextStep("two").Resp, nil
	}})
	res2, err := NewRunner().RunWithHistory(context.Background(), newAgent(second), res1.Messages, "more")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Output != "two" {
		t.Fatalf("output = %q", res2.Output)
	}
}

func TestNewRunnerDefaults(t *testing.T) {
	r := NewRunner()
	if r.MaxTurns != defaultMaxTurns {
		t.Fatalf("MaxTurns = %d", r.MaxTurns)
	}
	r2 := &Runner{} // zero-value Runner must still work
	m := testutil.NewScripted(testutil.TextStep("ok"))
	res, err := r2.Run(context.Background(), newAgent(m), "x")
	if err != nil || res.Output != "ok" {
		t.Fatalf("zero-value runner: %v, %v", res, err)
	}
}

func BenchmarkRunnerTextTurn(b *testing.B) {
	bm := model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		return &model.Response{
			Message:      model.Message{Role: model.RoleAssistant, Content: "ok"},
			FinishReason: "stop",
		}, nil
	})
	r := NewRunner()
	a := newAgent(bm)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Run(ctx, a, "x"); err != nil {
			b.Fatal(err)
		}
	}
}
