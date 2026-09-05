package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

func userMsg(s string) model.Message {
	return model.Message{Role: model.RoleUser, Content: s}
}

func assistantMsg(s string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: s}
}

func rangeMsgs(n int) []model.Message {
	out := make([]model.Message, n)
	for i := range out {
		out[i] = userMsg(fmt.Sprintf("msg-%d", i+1))
	}
	return out
}

func contents(msgs []model.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func assertContents(t *testing.T, got []model.Message, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %v", len(got), len(want), contents(got))
	}
	for i := range want {
		if got[i].Content != want[i] {
			t.Fatalf("message %d = %q, want %q", i, got[i].Content, want[i])
		}
	}
}

// runSessionCases exercises the agent.Session contract shared by all
// implementations.
func runSessionCases(t *testing.T, new func(t *testing.T) agent.Session) {
	t.Helper()

	t.Run("add get limit clear", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		assertContents(t, mustGet(t, ctx, s, 0))

		if err := s.AddItems(ctx, rangeMsgs(3)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		assertContents(t, mustGet(t, ctx, s, 0), "msg-1", "msg-2", "msg-3")
		assertContents(t, mustGet(t, ctx, s, 2), "msg-2", "msg-3")
		assertContents(t, mustGet(t, ctx, s, 1), "msg-3")
		assertContents(t, mustGet(t, ctx, s, 99), "msg-1", "msg-2", "msg-3")

		if err := s.Clear(ctx); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		assertContents(t, mustGet(t, ctx, s, 0))
	})

	t.Run("add items keeps appending", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		if err := s.AddItems(ctx, rangeMsgs(2)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		if err := s.AddItems(ctx, []model.Message{userMsg("msg-3")}); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		if err := s.AddItems(ctx, nil); err != nil {
			t.Fatalf("AddItems empty: %v", err)
		}
		assertContents(t, mustGet(t, ctx, s, 0), "msg-1", "msg-2", "msg-3")
	})

	t.Run("stored history is insulated from mutations", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		items := []model.Message{assistantMsg("original")}
		items[0].ToolCalls = []model.ToolCall{{ID: "c1", Function: model.FunctionCall{Name: "t", Arguments: `{"a":1}`}}}
		if err := s.AddItems(ctx, items); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		items[0].Content = "mutated"
		items[0].ToolCalls[0].Function.Arguments = "mutated"

		got := mustGet(t, ctx, s, 0)
		if got[0].Content != "original" || got[0].ToolCalls[0].Function.Arguments != `{"a":1}` {
			t.Fatalf("stored message was mutated: %+v", got[0])
		}

		got[0].Content = "caller-mutated"
		got2 := mustGet(t, ctx, s, 0)
		if got2[0].Content != "original" {
			t.Fatalf("GetItems result aliases storage: %q", got2[0].Content)
		}
	})

	t.Run("messages round-trip through storage", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		in := []model.Message{
			{Role: model.RoleUser, Content: "q", Name: "caller"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Function: model.FunctionCall{Name: "search", Arguments: `{"query":"go"}`}}}},
			{Role: model.RoleTool, ToolCallID: "c1", Name: "search", Content: "result"},
			{Role: model.RoleAssistant, Parts: []model.ContentPart{{Type: "text", Text: "multi\nline \"quoted\""}}},
		}
		if err := s.AddItems(ctx, in); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		got := mustGet(t, ctx, s, 0)
		for i := range in {
			if got[i].Role != in[i].Role || got[i].Content != in[i].Content ||
				got[i].Name != in[i].Name || got[i].ToolCallID != in[i].ToolCallID {
				t.Fatalf("message %d = %+v, want %+v", i, got[i], in[i])
			}
			if len(got[i].ToolCalls) != len(in[i].ToolCalls) || len(got[i].Parts) != len(in[i].Parts) {
				t.Fatalf("message %d slices: got %+v want %+v", i, got[i], in[i])
			}
		}
		if got[1].ToolCalls[0].Function.Name != "search" || got[3].Parts[0].Text != "multi\nline \"quoted\"" {
			t.Fatalf("tool call / part round-trip broken: %+v %+v", got[1], got[3])
		}
	})
}

func mustGet(t *testing.T, ctx context.Context, s agent.Session, limit int) []model.Message {
	t.Helper()
	msgs, err := s.GetItems(ctx, limit)
	if err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	return msgs
}

func TestInMemory(t *testing.T) {
	runSessionCases(t, func(t *testing.T) agent.Session {
		return NewInMemory()
	})
}

func TestFile(t *testing.T) {
	runSessionCases(t, func(t *testing.T) agent.Session {
		return NewFile(filepath.Join(t.TempDir(), "session.jsonl"))
	})

	t.Run("missing file reads as empty and clear succeeds", func(t *testing.T) {
		ctx := context.Background()
		s := NewFile(filepath.Join(t.TempDir(), "absent.jsonl"))
		assertContents(t, mustGet(t, ctx, s, 0))
		if err := s.Clear(ctx); err != nil {
			t.Fatalf("Clear on missing file: %v", err)
		}
	})

	t.Run("history survives a new instance", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "session.jsonl")
		first := NewFile(path)
		if err := first.AddItems(ctx, rangeMsgs(3)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		second := NewFile(path)
		assertContents(t, mustGet(t, ctx, second, 0), "msg-1", "msg-2", "msg-3")
		assertContents(t, mustGet(t, ctx, second, 2), "msg-2", "msg-3")
	})

	t.Run("bad json line reports the line number", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "broken.jsonl")
		s := NewFile(path)
		if err := s.AddItems(ctx, rangeMsgs(2)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := f.WriteString("this is not json\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = f.Close()

		_, err = s.GetItems(ctx, 0)
		if err == nil {
			t.Fatal("expected error for corrupt line")
		}
		if !strings.Contains(err.Error(), "line 3") {
			t.Fatalf("error should mention line 3, got: %v", err)
		}
	})
}

func TestSQLiteStore(t *testing.T) {
	runSessionCases(t, func(t *testing.T) agent.Session {
		return openStore(t).Get("case")
	})

	t.Run("keyed sessions are isolated", func(t *testing.T) {
		store := openStore(t)
		ctx := context.Background()
		a, b := store.Get("alpha"), store.Get("beta")

		if err := a.AddItems(ctx, rangeMsgs(3)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		assertContents(t, mustGet(t, ctx, b, 0))

		if err := b.AddItems(ctx, []model.Message{userMsg("beta-1")}); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		assertContents(t, mustGet(t, ctx, a, 0), "msg-1", "msg-2", "msg-3")
		assertContents(t, mustGet(t, ctx, b, 0), "beta-1")
	})

	t.Run("history survives reopen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sessions.db")
		first, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		if err := first.Get("durable").AddItems(context.Background(), rangeMsgs(3)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		second, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer func() { _ = second.Close() }()
		assertContents(t, mustGet(t, context.Background(), second.Get("durable"), 0), "msg-1", "msg-2", "msg-3")
	})

	t.Run("clear only touches its own session", func(t *testing.T) {
		store := openStore(t)
		ctx := context.Background()
		a, b := store.Get("alpha"), store.Get("beta")
		if err := a.AddItems(ctx, rangeMsgs(2)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		if err := b.AddItems(ctx, rangeMsgs(2)); err != nil {
			t.Fatalf("AddItems: %v", err)
		}
		if err := a.Clear(ctx); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		assertContents(t, mustGet(t, ctx, a, 0))
		assertContents(t, mustGet(t, ctx, b, 0), "msg-1", "msg-2")
	})

	t.Run("concurrent adders keep every message", func(t *testing.T) {
		store := openStore(t)
		ctx := context.Background()
		s := store.Get("contended")

		const workers, per = 8, 5
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < per; i++ {
					if err := s.AddItems(ctx, []model.Message{userMsg(fmt.Sprintf("w%d-i%d", w, i))}); err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent AddItems: %v", err)
		}

		got := mustGet(t, ctx, s, 0)
		if len(got) != workers*per {
			t.Fatalf("got %d messages, want %d", len(got), workers*per)
		}
		seen := map[string]bool{}
		for _, m := range got {
			seen[m.Content] = true
		}
		if len(seen) != workers*per {
			t.Fatalf("duplicated messages: %d unique of %d", len(seen), workers*per)
		}
	})
}

func openStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// --- Runner integration with real sessions ---

func TestRunWithSessionTwoTurns(t *testing.T) {
	ctx := context.Background()
	var secondPrompt []model.Message
	m := testutil.NewScripted(
		testutil.TextStep("first answer"),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			secondPrompt = append([]model.Message(nil), req.Messages...)
			return testutil.TextStep("second answer").Resp, nil
		}},
	)
	a := &agent.Agent{Name: "chatter", Instructions: "You are helpful.", Model: m}
	sess := NewInMemory()

	res1, err := agent.NewRunner().RunWithSession(ctx, a, sess, "hello")
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.Output != "first answer" {
		t.Fatalf("run 1 output = %q", res1.Output)
	}

	res2, err := agent.NewRunner().RunWithSession(ctx, a, sess, "again")
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Output != "second answer" {
		t.Fatalf("run 2 output = %q", res2.Output)
	}

	// The second model call must see: system + turn 1 (user+assistant) + input.
	assertContents(t, secondPrompt, "You are helpful.", "hello", "first answer", "again")
	if secondPrompt[0].Role != model.RoleSystem {
		t.Fatalf("first message role = %q, want system", secondPrompt[0].Role)
	}

	// The session stores the transcript without the system prompt.
	got := mustGet(t, ctx, sess, 0)
	assertContents(t, got, "hello", "first answer", "again", "second answer")
	if got[0].Role != model.RoleUser || got[1].Role != model.RoleAssistant {
		t.Fatalf("unexpected roles: %s / %s", got[0].Role, got[1].Role)
	}
}

func TestRunWithSessionFailedRunNotPersisted(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewScripted(testutil.HTTPErrorStep(500, "boom"))
	a := &agent.Agent{Model: m}
	sess := NewInMemory()

	if _, err := agent.NewRunner().RunWithSession(ctx, a, sess, "hello"); err == nil {
		t.Fatal("expected run failure")
	}
	if got := mustGet(t, ctx, sess, 0); len(got) != 0 {
		t.Fatalf("failed run persisted %d messages", len(got))
	}
}

func TestRunStreamWithSession(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewScriptedStream(
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("hi")}, FinishReason: "stop"},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("again!")}, FinishReason: "stop"},
	)
	a := &agent.Agent{Name: "streamer", Model: m}
	sess := NewInMemory()

	res1, err := agent.NewRunner().RunStreamWithSession(ctx, a, sess, "hello").Wait()
	if err != nil {
		t.Fatalf("stream run 1: %v", err)
	}
	if res1.Output != "hi" {
		t.Fatalf("run 1 output = %q", res1.Output)
	}

	res2, err := agent.NewRunner().RunStreamWithSession(ctx, a, sess, "more").Wait()
	if err != nil {
		t.Fatalf("stream run 2: %v", err)
	}
	if res2.Output != "again!" {
		t.Fatalf("run 2 output = %q", res2.Output)
	}

	// The second model call saw turn 1 without the system prompt.
	assertContents(t, m.LastRequest().Messages, "hello", "hi", "more")
	assertContents(t, mustGet(t, ctx, sess, 0), "hello", "hi", "more", "again!")
}

func TestRunStreamWithSessionLoadError(t *testing.T) {
	sess := &failingSession{getErr: errors.New("disk gone")}
	r := agent.NewRunner().RunStreamWithSession(context.Background(),
		&agent.Agent{Model: testutil.NewScripted()}, sess, "hello")
	res, err := r.Wait()
	if err == nil {
		t.Fatal("expected load error")
	}
	if res != nil {
		t.Fatalf("expected nil result, got %+v", res)
	}
	if !strings.Contains(err.Error(), "load session") {
		t.Fatalf("error should mention load session: %v", err)
	}
}

type failingSession struct {
	getErr error
	addErr error
}

func (f *failingSession) GetItems(ctx context.Context, limit int) ([]model.Message, error) {
	return nil, f.getErr
}

func (f *failingSession) AddItems(ctx context.Context, items []model.Message) error {
	return f.addErr
}

func (f *failingSession) Clear(ctx context.Context) error { return nil }

func TestRunWithSessionCompressor(t *testing.T) {
	ctx := context.Background()
	sess := NewInMemory()
	if err := sess.AddItems(ctx, rangeMsgs(6)); err != nil {
		t.Fatalf("AddItems: %v", err)
	}

	m := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		return testutil.TextStep("done").Resp, nil
	}})
	r := agent.NewRunner()
	r.Compressor = NewSlidingWindow(2)

	res, err := r.RunWithSession(ctx, &agent.Agent{Model: m}, sess, "question")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The model sees only the compressed view: last 2 + input.
	assertContents(t, m.LastRequest().Messages, "msg-5", "msg-6", "question")
	// res.Messages reflects the view the model actually saw.
	assertContents(t, res.Messages, "msg-5", "msg-6", "question", "done")
	// The session stays lossless and receives only the new messages.
	assertContents(t, mustGet(t, ctx, sess, 0),
		"msg-1", "msg-2", "msg-3", "msg-4", "msg-5", "msg-6", "question", "done")
}

func TestRunWithSessionAddItemsError(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewScripted(testutil.TextStep("fine"))
	sess := &failingSession{addErr: errors.New("disk full")}

	_, err := agent.NewRunner().RunWithSession(ctx, &agent.Agent{Model: m}, sess, "hello")
	if err == nil {
		t.Fatal("expected writeback failure")
	}
	if !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("error should mention persist session: %v", err)
	}
}

func TestRunWithSessionNilSession(t *testing.T) {
	m := testutil.NewScripted(testutil.TextStep("never"))
	_, err := agent.NewRunner().RunWithSession(context.Background(), &agent.Agent{Model: m}, nil, "hi")
	if err == nil || !strings.Contains(err.Error(), "session is nil") {
		t.Fatalf("expected nil session error, got: %v", err)
	}
}
