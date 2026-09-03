package model_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jiangfufa233/openai-agent-sdk-go/model"
)

func TestNewStreamFromResponse(t *testing.T) {
	resp := &model.Response{
		ID:           "cmpl-1",
		Model:        "gpt-test",
		Message:      model.Message{Role: model.RoleAssistant, Content: "hello world"},
		FinishReason: "stop",
		Usage:        model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}
	sr := model.NewStreamFromResponse(resp)
	defer func() { _ = sr.Close() }()

	var got []model.StreamEvent
	for sr.Next() {
		got = append(got, sr.Event())
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Type != model.StreamTextDelta || got[0].Text != "hello world" {
		t.Errorf("event 0 = %+v", got[0])
	}
	fin := got[1]
	if fin.Type != model.StreamFinish || fin.FinishReason != "stop" ||
		fin.ID != "cmpl-1" || fin.Model != "gpt-test" || fin.Usage.TotalTokens != 5 {
		t.Errorf("event 1 = %+v", fin)
	}
	// Next after exhaustion keeps returning false; Close is idempotent.
	if sr.Next() {
		t.Error("Next after exhaustion = true")
	}
	if err := sr.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}

func TestNewStreamFromResponseEmptyContent(t *testing.T) {
	sr := model.NewStreamFromResponse(&model.Response{FinishReason: "tool_calls"})
	var got []model.StreamEvent
	for sr.Next() {
		got = append(got, sr.Event())
	}
	if len(got) != 1 || got[0].Type != model.StreamFinish || got[0].FinishReason != "tool_calls" {
		t.Fatalf("got %+v", got)
	}
}

type failingModel struct{ err error }

func (f failingModel) Chat(context.Context, *model.Request) (*model.Response, error) {
	return nil, f.err
}

func TestAsStreamPropagatesRequestError(t *testing.T) {
	boom := errors.New("boom")
	sm := model.AsStream(failingModel{err: boom})
	if _, err := sm.ChatStream(context.Background(), &model.Request{}); !errors.Is(err, boom) {
		t.Fatalf("ChatStream err = %v, want %v", err, boom)
	}
}

func TestAsStreamEmitsFullResponse(t *testing.T) {
	base := model.ModelFunc(func(context.Context, *model.Request) (*model.Response, error) {
		return &model.Response{
			Message:      model.Message{Role: model.RoleAssistant, Content: "full text"},
			FinishReason: "stop",
		}, nil
	})
	sm := model.AsStream(base)
	// AsStream keeps the underlying Model usable directly.
	if _, err := sm.Chat(context.Background(), &model.Request{}); err != nil {
		t.Fatalf("Chat = %v", err)
	}
	sr, err := sm.ChatStream(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("ChatStream = %v", err)
	}
	defer func() { _ = sr.Close() }()
	var got []model.StreamEvent
	for sr.Next() {
		got = append(got, sr.Event())
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if len(got) != 2 || got[0].Type != model.StreamTextDelta || got[0].Text != "full text" ||
		got[1].Type != model.StreamFinish || got[1].FinishReason != "stop" {
		t.Fatalf("got %+v", got)
	}
}

func TestToolCallAccumulator(t *testing.T) {
	var acc model.ToolCallAccumulator
	// Interleaved indexes, split arguments, a repeated ID.
	acc.Add(model.ToolCall{Index: 0, ID: "call_a", Function: model.FunctionCall{Name: "get_weather"}})
	acc.Add(model.ToolCall{Index: 1, ID: "call_b", Function: model.FunctionCall{Name: "lookup"}})
	acc.Add(model.ToolCall{Index: 0, Function: model.FunctionCall{Arguments: `{"city":"Bei`}})
	acc.Add(model.ToolCall{Index: 1, Function: model.FunctionCall{Arguments: `{"q":"x"}`}})
	acc.Add(model.ToolCall{Index: 0, Function: model.FunctionCall{Arguments: `jing"}`}})
	acc.Add(model.ToolCall{Index: 0, ID: "call_a"}) // repeat keeps state stable

	got := acc.Calls()
	want := []model.ToolCall{
		{Index: 0, ID: "call_a", Function: model.FunctionCall{Name: "get_weather", Arguments: `{"city":"Beijing"}`}},
		{Index: 1, ID: "call_b", Function: model.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d calls, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestToolCallAccumulatorEmpty(t *testing.T) {
	var acc model.ToolCallAccumulator
	if calls := acc.Calls(); calls != nil {
		t.Fatalf("Calls on empty accumulator = %+v, want nil", calls)
	}
}
