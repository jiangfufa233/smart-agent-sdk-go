package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/testutil"
	"github.com/example/agent-sdk/tool"
)

// drain collects all events of a stream run.
func drain(t *testing.T, sr *StreamRun) ([]StreamEvent, *RunResult, error) {
	t.Helper()
	var evs []StreamEvent
	for ev := range sr.Events {
		evs = append(evs, ev)
	}
	res, err := sr.Result()
	return evs, res, err
}

// requireTypes asserts the exact event type sequence.
func requireTypes(t *testing.T, evs []StreamEvent, want ...StreamEventType) {
	t.Helper()
	got := make([]StreamEventType, 0, len(evs))
	for _, ev := range evs {
		got = append(got, ev.Type)
	}
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event types = %v, want %v (mismatch at %d)", got, want, i)
		}
	}
}

func TestRunStreamFallbackNonStreaming(t *testing.T) {
	a := newAgent(testutil.NewScripted(testutil.TextStep("hello")))
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), a, "hi"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	requireTypes(t, evs,
		StreamRunStarted, StreamTextDelta, StreamFinalOutput)
	if evs[1].Text != "hello" || evs[2].Text != "hello" {
		t.Fatalf("text events = %q / %q", evs[1].Text, evs[2].Text)
	}
	if res.Output != "hello" || res.RunID == "" || res.Duration <= 0 {
		t.Fatalf("result = %+v", res)
	}
	// RunResult parity with the non-streaming Run (fresh script).
	runRes, err := NewRunner().Run(context.Background(), newAgent(testutil.NewScripted(testutil.TextStep("hello"))), "hi")
	if err != nil || runRes.Output != res.Output || len(runRes.Messages) != len(res.Messages) {
		t.Fatalf("Run parity: (%+v, %v) vs (%+v, %v)", runRes, err, res, nil)
	}
}

func TestRunStreamTextDeltas(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("Hel"), testutil.TextChunk("lo")},
		FinishReason: "stop",
		Usage:        model.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
	})
	a := newAgent(m)
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), a, "hi"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	requireTypes(t, evs, StreamRunStarted, StreamTextDelta, StreamTextDelta, StreamFinalOutput)
	if evs[1].Text != "Hel" || evs[2].Text != "lo" {
		t.Fatalf("deltas = %q %q", evs[1].Text, evs[2].Text)
	}
	fin := evs[3]
	if fin.Text != "Hello" || fin.FinishReason != "stop" || fin.Usage.TotalTokens != 12 {
		t.Fatalf("final = %+v", fin)
	}
	if res.Output != "Hello" || res.Usage.TotalTokens != 12 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunStreamToolLoop(t *testing.T) {
	var gotArgs string
	capture, _ := tool.NewFunction("get_weather", "weather",
		func(ctx context.Context, in cityArgs) (string, error) {
			gotArgs = in.City
			return `{"temp_c":21}`, nil
		})
	m := testutil.NewScriptedStream(
		testutil.StreamStep{
			Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{Index: 0, ID: "c1", Type: "function",
					Function: model.FunctionCall{Name: "get_weather"}}),
				testutil.ToolCallChunk(model.ToolCall{Index: 0, Function: model.FunctionCall{Arguments: `{"city":"Bei`}}),
				testutil.ToolCallChunk(model.ToolCall{Index: 0, Function: model.FunctionCall{Arguments: `jing"}`}}),
			},
			FinishReason: "tool_calls",
			Usage:        model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
		testutil.StreamStep{
			Deltas:       []model.StreamEvent{testutil.TextChunk("21C")},
			FinishReason: "stop",
			Usage:        model.Usage{PromptTokens: 30, CompletionTokens: 2, TotalTokens: 32},
		},
	)
	a := newAgent(m, capture)
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), a, "weather?"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	requireTypes(t, evs,
		StreamRunStarted,
		StreamToolCallStarted, StreamToolCallArgs, StreamToolCallArgs, StreamToolCallFinished,
		StreamToolResult,
		StreamTextDelta, StreamFinalOutput)

	if evs[1].Call.ID != "c1" || evs[1].Call.Function.Name != "get_weather" {
		t.Errorf("started = %+v", evs[1].Call)
	}
	if evs[2].Call.Function.Arguments != `{"city":"Bei` || evs[3].Call.Function.Arguments != `jing"}` {
		t.Errorf("arg fragments = %q %q", evs[2].Call.Function.Arguments, evs[3].Call.Function.Arguments)
	}
	if evs[4].Call.Function.Arguments != `{"city":"Beijing"}` {
		t.Errorf("finished = %+v", evs[4].Call)
	}
	if evs[5].Call.ID != "c1" || evs[5].ToolErr != nil || evs[5].Result != `{"temp_c":21}` {
		t.Errorf("result = %+v", evs[5])
	}
	if gotArgs != "Beijing" {
		t.Errorf("tool got city %q, want Beijing", gotArgs)
	}
	if res.Usage.TotalTokens != 47 { // 15 + 32 accumulated across turns
		t.Errorf("usage = %+v, want total 47", res.Usage)
	}
	// The conversation history must contain the assembled assistant message
	// and the tool result, same as a non-streaming run.
	var sawToolMsg bool
	for _, msg := range res.Messages {
		if msg.Role == model.RoleTool && msg.ToolCallID == "c1" && msg.Content == `{"temp_c":21}` {
			sawToolMsg = true
		}
	}
	if !sawToolMsg {
		t.Errorf("missing tool result message in history: %+v", res.Messages)
	}
}

func TestRunStreamToolErrorParity(t *testing.T) {
	m := testutil.NewScriptedStream(
		testutil.StreamStep{
			Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{Index: 0, ID: "c1", Type: "function",
					Function: model.FunctionCall{Name: "boom", Arguments: "{}"}}),
			},
			FinishReason: "tool_calls",
		},
		testutil.StreamStep{FinishReason: "stop"},
	)
	a := newAgent(m, failingTool("boom", errors.New("exploded")))
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), a, "hi"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	requireTypes(t, evs,
		StreamRunStarted, StreamToolCallStarted, StreamToolCallArgs, StreamToolCallFinished,
		StreamToolResult, StreamFinalOutput)
	if evs[4].ToolErr == nil || evs[4].ToolErr.Tool != "boom" {
		t.Fatalf("tool result event = %+v", evs[4])
	}
	if len(res.ToolErrors) != 1 {
		t.Fatalf("ToolErrors = %+v", res.ToolErrors)
	}
}

func TestRunStreamHooksSequence(t *testing.T) {
	rec := &recorder{}
	m := testutil.NewScriptedStream(
		testutil.StreamStep{
			Deltas: []model.StreamEvent{
				testutil.ToolCallChunk(model.ToolCall{Index: 0, ID: "c1", Type: "function",
					Function: model.FunctionCall{Name: "get_weather", Arguments: "{}"}}),
			},
			FinishReason: "tool_calls",
		},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("ok")}, FinishReason: "stop"},
	)
	r := NewRunner()
	r.Hooks = rec
	a := newAgent(m, weatherTool())
	if _, _, err := drain(t, r.RunStream(context.Background(), a, "hi")); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{
		"run_start",
		"llm_call:0", "llm_response",
		"tool_call:get_weather", "tool_result",
		"llm_call:1", "llm_response",
		"run_end",
	}
	if len(rec.events) != len(want) {
		t.Fatalf("hooks = %v, want %v", rec.events, want)
	}
	for i := range want {
		if rec.events[i] != want[i] {
			t.Fatalf("hooks = %v, want %v", rec.events, want)
		}
	}
}

func TestRunStreamMaxTurns(t *testing.T) {
	infinite := model.ModelFunc(func(ctx context.Context, req *model.Request) (*model.Response, error) {
		return &model.Response{
			Message: model.Message{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID: "c", Type: "function",
					Function: model.FunctionCall{Name: "get_weather", Arguments: "{}"},
				}},
			},
			FinishReason: "tool_calls",
		}, nil
	})
	r := NewRunner()
	r.MaxTurns = 2
	evs, res, err := drain(t, r.RunStream(context.Background(), newAgent(infinite, weatherTool()), "hi"))
	if res != nil || !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("result = (%+v, %v), want ErrMaxTurns", res, err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != StreamRunError {
		t.Fatalf("last event = %+v, want StreamRunError", evs)
	}
	if !errors.Is(evs[len(evs)-1].Err, ErrMaxTurns) {
		t.Fatalf("terminal event err = %v", evs[len(evs)-1].Err)
	}
}

func TestRunStreamMidStreamError(t *testing.T) {
	boom := &model.ModelError{Kind: model.ErrorProtocol, Provider: "scripted", Err: errors.New("connection broke")}
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:    []model.StreamEvent{testutil.TextChunk("partial")},
		StreamErr: boom,
	})
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), newAgent(m), "hi"))
	if res != nil {
		t.Fatalf("result = %+v, want nil", res)
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorProtocol {
		t.Fatalf("err = %v, want protocol ModelError", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != StreamRunError {
		t.Fatalf("events = %+v", evs)
	}
}

func TestRunStreamRequestError(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Err: model.NewHTTPError("scripted", 429, "slow down"),
	})
	evs, _, err := drain(t, NewRunner().RunStream(context.Background(), newAgent(m), "hi"))
	if err == nil {
		t.Fatal("want error")
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorRateLimited {
		t.Fatalf("err = %v, want rate_limited", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != StreamRunError {
		t.Fatalf("events = %+v", evs)
	}
}

func TestRunStreamNoModel(t *testing.T) {
	evs, res, err := drain(t, NewRunner().RunStream(context.Background(), &Agent{Name: "x"}, "hi"))
	if res != nil || err == nil {
		t.Fatalf("result = (%+v, %v), want error", res, err)
	}
	requireTypes(t, evs, StreamRunError)
}

func TestRunStreamCancelMidStream(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:     []model.StreamEvent{testutil.TextChunk("a"), testutil.TextChunk("b"), testutil.TextChunk("c")},
		DeltaDelay: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	sr := NewRunner().RunStream(ctx, newAgent(m), "hi")

	<-sr.Events // run_started
	<-sr.Events // first delta
	cancel()
	res, err := sr.Wait()
	if res != nil {
		t.Fatalf("result = %+v, want nil", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// TestMain's goleak check verifies the producer goroutine exited.
}

func TestRunStreamWithHistory(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("again")},
		FinishReason: "stop",
	})
	a := &Agent{Name: "chat", Model: m} // no instructions
	history := []model.Message{
		{Role: model.RoleSystem, Content: "sys"},
		{Role: model.RoleUser, Content: "first"},
		{Role: model.RoleAssistant, Content: "answer"},
	}
	_, res, err := drain(t, NewRunner().RunStreamWithHistory(context.Background(), a, history, "second"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Messages) != 5 { // 3 history + new user + assistant
		t.Fatalf("messages = %d, want 5", len(res.Messages))
	}
	req := m.LastRequest()
	if req == nil || len(req.Messages) != 4 ||
		req.Messages[0].Content != "sys" || req.Messages[3].Content != "second" {
		t.Fatalf("request messages = %+v", req.Messages)
	}
}
