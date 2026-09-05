package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

// fakeSession records writebacks and can fail loads or writes.
type fakeSession struct {
	mu     sync.Mutex
	items  []model.Message
	adds   [][]model.Message
	getErr error
	addErr error
}

func (f *fakeSession) GetItems(ctx context.Context, limit int) ([]model.Message, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	msgs := append([]model.Message(nil), f.items...)
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

func (f *fakeSession) AddItems(ctx context.Context, items []model.Message) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds = append(f.adds, append([]model.Message(nil), items...))
	f.items = append(f.items, items...)
	return nil
}

func (f *fakeSession) Clear(ctx context.Context) error { return nil }

func assertMsgs(t *testing.T, got []model.Message, want ...model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Content != w.Content || g.ToolCallID != w.ToolCallID ||
			len(g.ToolCalls) != len(w.ToolCalls) {
			t.Fatalf("message %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestRunWithSessionPersistsNewMessages(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{}
	a := &Agent{Model: testutil.NewScripted(testutil.TextStep("hi there"))}

	if _, err := NewRunner().RunWithSession(ctx, a, s, "hello"); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if len(s.adds) != 1 {
		t.Fatalf("AddItems called %d times, want 1", len(s.adds))
	}
	assertMsgs(t, s.adds[0],
		model.Message{Role: model.RoleUser, Content: "hello"},
		model.Message{Role: model.RoleAssistant, Content: "hi there"},
	)
}

func TestRunWithSessionHandoffTranscript(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{}
	specialist := &Agent{Name: "specialist", Model: testutil.NewScripted(testutil.TextStep("spec done"))}
	supervisor := &Agent{
		Name:     "supervisor",
		Model:    testutil.NewScripted(testutil.ToolCallStep("t1", "transfer_to_specialist", "{}")),
		Handoffs: []Handoff{{Target: specialist}},
	}

	res, err := NewRunner().RunWithSession(ctx, supervisor, s, "route me")
	if err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if len(res.Transfers) != 1 || res.Transfers[0] != "specialist" {
		t.Fatalf("transfers = %v", res.Transfers)
	}
	// The whole transcript — including the handoff marker result — is
	// persisted so the conversation can continue afterwards.
	assertMsgs(t, s.adds[0],
		model.Message{Role: model.RoleUser, Content: "route me"},
		model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
			ID: "t1", Function: model.FunctionCall{Name: "transfer_to_specialist", Arguments: "{}"},
		}}},
		model.Message{Role: model.RoleTool, ToolCallID: "t1", Name: "transfer_to_specialist", Content: `{"handoff_to":"specialist"}`},
		model.Message{Role: model.RoleAssistant, Content: "spec done"},
	)
}

func TestRunWithSessionInstructionsNotStored(t *testing.T) {
	ctx := context.Background()
	var prompt []model.Message
	s := &fakeSession{items: []model.Message{{Role: model.RoleUser, Content: "old"}}}
	a := &Agent{
		Instructions: "be nice",
		Model: testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			prompt = append([]model.Message(nil), req.Messages...)
			return testutil.TextStep("ok").Resp, nil
		}}),
	}

	if _, err := NewRunner().RunWithSession(ctx, a, s, "new input"); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	// History loaded from the session, instructions prepended once.
	assertMsgs(t, prompt,
		model.Message{Role: model.RoleSystem, Content: "be nice"},
		model.Message{Role: model.RoleUser, Content: "old"},
		model.Message{Role: model.RoleUser, Content: "new input"},
	)
	// Only the new messages are written back — no system prompt, no "old".
	assertMsgs(t, s.adds[0],
		model.Message{Role: model.RoleUser, Content: "new input"},
		model.Message{Role: model.RoleAssistant, Content: "ok"},
	)
}

// trimmingCompressor keeps the first two messages and records every call.
type trimmingCompressor struct {
	mu  sync.Mutex
	got [][]model.Message
}

func (c *trimmingCompressor) Compress(ctx context.Context, history []model.Message) ([]model.Message, error) {
	c.mu.Lock()
	c.got = append(c.got, append([]model.Message(nil), history...))
	c.mu.Unlock()
	if len(history) > 2 {
		return history[:2], nil
	}
	return history, nil
}

func TestCompressorAppliesToRunWithHistory(t *testing.T) {
	ctx := context.Background()
	comp := &trimmingCompressor{}
	history := []model.Message{
		{Role: model.RoleUser, Content: "q1"},
		{Role: model.RoleAssistant, Content: "a1"},
		{Role: model.RoleUser, Content: "q2"},
		{Role: model.RoleAssistant, Content: "a2"},
	}
	m := testutil.NewScripted(testutil.TextStep("done"))
	r := NewRunner()
	r.Compressor = comp

	res, err := r.RunWithHistory(ctx, &Agent{Model: m}, history, "q3")
	if err != nil {
		t.Fatalf("RunWithHistory: %v", err)
	}
	if len(comp.got) != 1 || len(comp.got[0]) != 4 {
		t.Fatalf("compressor received %+v", comp.got)
	}
	// The model saw the compressed view plus the input.
	assertMsgs(t, m.LastRequest().Messages,
		model.Message{Role: model.RoleUser, Content: "q1"},
		model.Message{Role: model.RoleAssistant, Content: "a1"},
		model.Message{Role: model.RoleUser, Content: "q3"},
	)
	// res.Messages reflects the view the model actually saw.
	assertMsgs(t, res.Messages,
		model.Message{Role: model.RoleUser, Content: "q1"},
		model.Message{Role: model.RoleAssistant, Content: "a1"},
		model.Message{Role: model.RoleUser, Content: "q3"},
		model.Message{Role: model.RoleAssistant, Content: "done"},
	)
}

func TestCompressorAppliesToSessionRun(t *testing.T) {
	ctx := context.Background()
	comp := &trimmingCompressor{}
	s := &fakeSession{items: []model.Message{
		{Role: model.RoleUser, Content: "q1"},
		{Role: model.RoleAssistant, Content: "a1"},
		{Role: model.RoleUser, Content: "q2"},
		{Role: model.RoleAssistant, Content: "a2"},
	}}
	m := testutil.NewScripted(testutil.TextStep("done"))
	r := NewRunner()
	r.Compressor = comp

	if _, err := r.RunWithSession(ctx, &Agent{Model: m}, s, "q3"); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	assertMsgs(t, m.LastRequest().Messages,
		model.Message{Role: model.RoleUser, Content: "q1"},
		model.Message{Role: model.RoleAssistant, Content: "a1"},
		model.Message{Role: model.RoleUser, Content: "q3"},
	)
}

func TestRunWithSessionErrors(t *testing.T) {
	ctx := context.Background()

	if _, err := NewRunner().RunWithSession(ctx, &Agent{Model: testutil.NewScripted()}, nil, "x"); err == nil ||
		!strings.Contains(err.Error(), "session is nil") {
		t.Fatalf("nil session: got %v", err)
	}

	_, err := NewRunner().RunWithSession(ctx, &Agent{Model: testutil.NewScripted()},
		&fakeSession{getErr: errors.New("disk gone")}, "x")
	if err == nil || !strings.Contains(err.Error(), "load session") {
		t.Fatalf("load error: got %v", err)
	}

	_, err = NewRunner().RunWithSession(ctx, &Agent{Model: testutil.NewScripted(testutil.TextStep("ok"))},
		&fakeSession{addErr: errors.New("disk full")}, "x")
	if err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("writeback error: got %v", err)
	}
}

func TestRunStreamWithSessionWriteback(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{}
	a := &Agent{Name: "streamer", Model: testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("hello!")},
		FinishReason: "stop",
	})}

	res, err := NewRunner().RunStreamWithSession(ctx, a, s, "greet").Wait()
	if err != nil {
		t.Fatalf("RunStreamWithSession: %v", err)
	}
	if res.Output != "hello!" {
		t.Fatalf("output = %q", res.Output)
	}
	assertMsgs(t, s.adds[0],
		model.Message{Role: model.RoleUser, Content: "greet"},
		model.Message{Role: model.RoleAssistant, Content: "hello!"},
	)
}

func TestRunStreamWithSessionLoadErrorSingleTerminal(t *testing.T) {
	run := NewRunner().RunStreamWithSession(context.Background(),
		&Agent{Model: testutil.NewScripted()}, &fakeSession{getErr: errors.New("gone")}, "x")
	types := collectStreamTypes(t, run)
	if n := countTerminals(types); n != 1 {
		t.Fatalf("got %d terminal events, want 1: %v", n, types)
	}
	if types[len(types)-1] != StreamRunError {
		t.Fatalf("last event = %v, want StreamRunError", types[len(types)-1])
	}
}

func TestRunStreamWithSessionWritebackErrorSingleTerminal(t *testing.T) {
	run := NewRunner().RunStreamWithSession(context.Background(),
		&Agent{Model: testutil.NewScriptedStream(testutil.StreamStep{
			Deltas:       []model.StreamEvent{testutil.TextChunk("x")},
			FinishReason: "stop",
		})},
		&fakeSession{addErr: errors.New("disk full")}, "x")
	types := collectStreamTypes(t, run)
	if n := countTerminals(types); n != 1 {
		t.Fatalf("got %d terminal events, want 1: %v", n, types)
	}
	if types[len(types)-1] != StreamRunError {
		t.Fatalf("last event = %v, want StreamRunError", types[len(types)-1])
	}
}
