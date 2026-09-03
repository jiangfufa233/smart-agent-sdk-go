package testutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-sdk/model"
)

func TestScriptedStreamChatMaterializesDeltas(t *testing.T) {
	m := NewScriptedStream(StreamStep{
		Deltas: []model.StreamEvent{
			TextChunk("Hel"),
			ToolCallChunk(model.ToolCall{Index: 0, ID: "c1", Type: "function",
				Function: model.FunctionCall{Name: "f", Arguments: `{"a"`}}),
			TextChunk("lo"),
			ToolCallChunk(model.ToolCall{Index: 0, Function: model.FunctionCall{Arguments: `:1}`}}),
			ToolCallChunk(model.ToolCall{Index: 1, ID: "c2", Type: "function",
				Function: model.FunctionCall{Name: "g", Arguments: "{}"}}),
		},
		FinishReason: "tool_calls",
		Usage:        model.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	})
	resp, err := m.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Hello" {
		t.Errorf("content = %q, want Hello", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 2 ||
		resp.Message.ToolCalls[0].ID != "c1" || resp.Message.ToolCalls[0].Function.Arguments != `{"a":1}` ||
		resp.Message.ToolCalls[1].ID != "c2" {
		t.Errorf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.TotalTokens != 3 {
		t.Errorf("finish/usage = %q / %+v", resp.FinishReason, resp.Usage)
	}
}

func TestScriptedStreamReaderSequence(t *testing.T) {
	m := NewScriptedStream(StreamStep{
		Deltas:       []model.StreamEvent{TextChunk("a"), TextChunk("b")},
		FinishReason: "stop",
		Usage:        model.Usage{TotalTokens: 9},
	})
	sr, err := m.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sr.Close() }()
	var texts []string
	var finish model.StreamEvent
	for sr.Next() {
		ev := sr.Event()
		switch ev.Type {
		case model.StreamTextDelta:
			texts = append(texts, ev.Text)
		case model.StreamFinish:
			finish = ev
		}
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if len(texts) != 2 || texts[0] != "a" || texts[1] != "b" {
		t.Errorf("texts = %v", texts)
	}
	if finish.FinishReason != "stop" || finish.Usage.TotalTokens != 9 {
		t.Errorf("finish = %+v", finish)
	}
	if sr.Next() {
		t.Error("Next after finish = true")
	}
}

func TestScriptedStreamRequestError(t *testing.T) {
	boom := errors.New("quota exceeded")
	if _, err := NewScriptedStream(StreamStep{Err: boom}).ChatStream(context.Background(), &model.Request{}); !errors.Is(err, boom) {
		t.Fatalf("ChatStream err = %v, want %v", err, boom)
	}
	if _, err := NewScriptedStream(StreamStep{Err: boom}).Chat(context.Background(), &model.Request{}); !errors.Is(err, boom) {
		t.Fatalf("Chat err = %v, want %v", err, boom)
	}
}

func TestScriptedStreamMidStreamError(t *testing.T) {
	boom := errors.New("connection reset")
	m := NewScriptedStream(StreamStep{
		Deltas:    []model.StreamEvent{TextChunk("partial")},
		StreamErr: boom,
	})
	sr, err := m.ChatStream(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sr.Close() }()
	if !sr.Next() {
		t.Fatalf("first Next = false, Err = %v", sr.Err())
	}
	for sr.Next() {
		t.Fatal("unexpected extra delta")
	}
	if !errors.Is(sr.Err(), boom) {
		t.Fatalf("Err = %v, want %v", sr.Err(), boom)
	}
}

func TestScriptedStreamDeltaDelayCancel(t *testing.T) {
	m := NewScriptedStream(StreamStep{
		Deltas:     []model.StreamEvent{TextChunk("a"), TextChunk("b")},
		DeltaDelay: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	sr, err := m.ChatStream(ctx, &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sr.Close() }()
	if !sr.Next() {
		t.Fatalf("first Next = false, Err = %v", sr.Err())
	}
	cancel()
	if sr.Next() {
		t.Fatal("Next after cancel = true, want abort")
	}
	if !errors.Is(sr.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", sr.Err())
	}
}

func TestScriptedStreamRecordsRequests(t *testing.T) {
	m := NewScriptedStream(StreamStep{FinishReason: "stop"}, StreamStep{FinishReason: "stop"})
	if _, err := m.Chat(context.Background(), &model.Request{Model: "m1", Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ChatStream(context.Background(), &model.Request{Model: "m2", Messages: []model.Message{{Role: model.RoleUser, Content: "y"}}}); err != nil {
		t.Fatal(err)
	}
	if m.Calls() != 2 {
		t.Fatalf("calls = %d, want 2", m.Calls())
	}
	last := m.LastRequest()
	if last == nil || last.Model != "m2" {
		t.Fatalf("last request = %+v", last)
	}
}

func TestScriptedStreamExhausted(t *testing.T) {
	m := NewScriptedStream(StreamStep{FinishReason: "stop"})
	if _, err := m.Chat(context.Background(), &model.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Chat(context.Background(), &model.Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("Chat err = %v, want ErrScriptExhausted", err)
	}
	if _, err := m.ChatStream(context.Background(), &model.Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("ChatStream err = %v, want ErrScriptExhausted", err)
	}
}
