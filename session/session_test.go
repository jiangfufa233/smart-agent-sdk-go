package session

import (
	"context"
	"database/sql"
	"encoding/json"
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

func contents(messages []model.Message) []string {
	out := make([]string, len(messages))
	for i, message := range messages {
		out[i] = message.Content
	}
	return out
}

func assertItemContents(t *testing.T, got []agent.SessionItem, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Message.Content != want[i] {
			t.Fatalf("message %d = %q, want %q", i, got[i].Message.Content, want[i])
		}
	}
}

func assertMessageContents(t *testing.T, got []model.Message, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(want), got)
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

	t.Run("append list limit clear", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		assertItemContents(t, mustList(t, ctx, s, 0))

		appended := mustAppend(t, ctx, s, rangeMsgs(3))
		if len(appended) != 3 || appended[0].ID == "" || appended[1].ID == "" || appended[0].ID == appended[1].ID {
			t.Fatalf("Append did not return unique generated IDs: %+v", appended)
		}
		assertItemContents(t, mustList(t, ctx, s, 0), "msg-1", "msg-2", "msg-3")
		assertItemContents(t, mustList(t, ctx, s, 2), "msg-2", "msg-3")
		assertItemContents(t, mustList(t, ctx, s, 1), "msg-3")
		assertItemContents(t, mustList(t, ctx, s, 99), "msg-1", "msg-2", "msg-3")

		if err := s.Clear(ctx); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		assertItemContents(t, mustList(t, ctx, s, 0))
	})

	t.Run("append keeps appending and empty input is a no-op", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		mustAppend(t, ctx, s, rangeMsgs(2))
		mustAppend(t, ctx, s, []model.Message{userMsg("msg-3")})
		if got := mustAppend(t, ctx, s, nil); len(got) != 0 {
			t.Fatalf("Append empty = %+v, want no generated items", got)
		}
		assertItemContents(t, mustList(t, ctx, s, 0), "msg-1", "msg-2", "msg-3")
	})

	t.Run("stored history and returned items are insulated from mutations", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		messages := []model.Message{assistantMsg("original")}
		messages[0].ToolCalls = []model.ToolCall{{ID: "c1", Function: model.FunctionCall{Name: "t", Arguments: `{"a":1}`}}}
		mustAppend(t, ctx, s, messages)
		messages[0].Content = "mutated"
		messages[0].ToolCalls[0].Function.Arguments = "mutated"

		got := mustList(t, ctx, s, 0)
		if got[0].Message.Content != "original" || got[0].Message.ToolCalls[0].Function.Arguments != `{"a":1}` {
			t.Fatalf("stored message was mutated: %+v", got[0])
		}
		got[0].ID = "caller-mutated"
		got[0].Message.Content = "caller-mutated"
		got2 := mustList(t, ctx, s, 0)
		if got2[0].ID == "caller-mutated" || got2[0].Message.Content != "original" {
			t.Fatalf("List result aliases storage: %+v", got2[0])
		}
	})

	t.Run("messages and model tool IDs round-trip", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		in := []model.Message{
			{Role: model.RoleUser, Content: "q", Name: "caller"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "model-call-1", Function: model.FunctionCall{Name: "search", Arguments: `{"query":"go"}`}}}},
			{Role: model.RoleTool, ToolCallID: "model-call-1", Name: "search", Content: "result"},
			{Role: model.RoleAssistant, Parts: []model.ContentPart{{Type: "text", Text: "multi\nline \"quoted\""}}},
		}
		appended := mustAppend(t, ctx, s, in)
		got := mustList(t, ctx, s, 0)
		for i := range in {
			if got[i].ID != appended[i].ID || got[i].ID == "model-call-1" ||
				got[i].Message.Role != in[i].Role || got[i].Message.Content != in[i].Content ||
				got[i].Message.Name != in[i].Name || got[i].Message.ToolCallID != in[i].ToolCallID {
				t.Fatalf("item %d = %+v, want message %+v", i, got[i], in[i])
			}
		}
		if got[1].Message.ToolCalls[0].ID != "model-call-1" || got[1].Message.ToolCalls[0].Function.Name != "search" ||
			got[2].Message.ToolCallID != "model-call-1" || got[3].Message.Parts[0].Text != "multi\nline \"quoted\"" {
			t.Fatalf("tool IDs or parts did not round-trip: %+v", got)
		}
	})

	t.Run("replace is a closed interval and retains unaffected IDs", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		original := mustAppend(t, ctx, s, rangeMsgs(4))
		replaced := mustReplace(t, ctx, s, original[1].ID, original[2].ID, []model.Message{assistantMsg("replacement")})
		if len(replaced) != 1 || replaced[0].ID == "" || replaced[0].ID == original[1].ID || replaced[0].ID == original[2].ID {
			t.Fatalf("Replace generated IDs = %+v", replaced)
		}
		got := mustList(t, ctx, s, 0)
		assertItemContents(t, got, "msg-1", "replacement", "msg-4")
		if got[0].ID != original[0].ID || got[1].ID != replaced[0].ID || got[2].ID != original[3].ID {
			t.Fatalf("Replace changed retained item IDs: %+v", got)
		}
		deleted := mustReplace(t, ctx, s, replaced[0].ID, replaced[0].ID, nil)
		if len(deleted) != 0 {
			t.Fatalf("Replace deletion returned %+v", deleted)
		}
		assertItemContents(t, mustList(t, ctx, s, 0), "msg-1", "msg-4")
	})

	t.Run("replace rejects unknown and reversed ranges atomically", func(t *testing.T) {
		ctx := context.Background()
		s := new(t)
		original := mustAppend(t, ctx, s, rangeMsgs(3))
		for _, tc := range []struct {
			first string
			last  string
		}{
			{first: "unknown", last: original[1].ID},
			{first: original[2].ID, last: original[0].ID},
		} {
			_, err := s.Replace(ctx, tc.first, tc.last, []model.Message{assistantMsg("must not persist")})
			var rangeErr *agent.SessionRangeError
			if !errors.As(err, &rangeErr) {
				t.Fatalf("Replace(%q, %q) error = %v, want *agent.SessionRangeError", tc.first, tc.last, err)
			}
			assertItemContents(t, mustList(t, ctx, s, 0), "msg-1", "msg-2", "msg-3")
		}
	})

	t.Run("canceled operations leave history unchanged", func(t *testing.T) {
		s := new(t)
		items := mustAppend(t, t.Context(), s, rangeMsgs(2))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		operations := []func() error{
			func() error { _, err := s.List(ctx, 0); return err },
			func() error { _, err := s.Append(ctx, nil); return err },
			func() error { _, err := s.Append(ctx, rangeMsgs(1)); return err },
			func() error { _, err := s.Replace(ctx, items[0].ID, items[1].ID, nil); return err },
			func() error { return s.Clear(ctx) },
		}
		for i, operation := range operations {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation %d: %v, want cancellation", i, err)
			}
		}
		assertItemContents(t, mustList(t, t.Context(), s, 0), "msg-1", "msg-2")
	})

	t.Run("replace preserves tool wire IDs and isolation", func(t *testing.T) {
		s := new(t)
		items := mustAppend(t, t.Context(), s, rangeMsgs(3))
		messages := []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "wire-id", Type: "function", Function: model.FunctionCall{Name: "read", Arguments: "{}"}}}},
			{Role: model.RoleTool, ToolCallID: "wire-id", Content: "file contents"},
			{Role: model.RoleUser, Parts: []model.ContentPart{{Type: model.PartImageURL, ImageURL: &model.ImageURL{URL: "image-data"}}}},
		}
		generated := mustReplace(t, t.Context(), s, items[0].ID, items[2].ID, messages)
		messages[0].ToolCalls[0].ID = "mutated"
		generated[2].Message.Parts[0].ImageURL.URL = "mutated"
		got := mustList(t, t.Context(), s, 0)
		if got[0].Message.ToolCalls[0].ID != "wire-id" || got[1].Message.ToolCallID != "wire-id" || got[2].Message.Parts[0].ImageURL.URL != "image-data" {
			t.Fatalf("replacement wire IDs or payload mutated: %+v", got)
		}
		mustReplace(t, t.Context(), s, got[0].ID, got[2].ID, nil)
		assertItemContents(t, mustList(t, t.Context(), s, 0))
	})

	t.Run("concurrent replace of one ID has one winner", func(t *testing.T) {
		s := new(t)
		original := mustAppend(t, t.Context(), s, rangeMsgs(3))
		const workers = 8
		errs := make(chan error, workers)
		for range workers {
			go func() {
				_, err := s.Replace(t.Context(), original[1].ID, original[1].ID, []model.Message{assistantMsg("summary")})
				errs <- err
			}()
		}
		winners := 0
		for range workers {
			err := <-errs
			if err == nil {
				winners++
				continue
			}
			var rangeErr *agent.SessionRangeError
			if !errors.As(err, &rangeErr) {
				t.Errorf("unexpected failure: %v", err)
			}
		}
		if winners != 1 {
			t.Fatalf("successful replacements = %d, want 1", winners)
		}
		assertItemContents(t, mustList(t, t.Context(), s, 0), "msg-1", "summary", "msg-3")
	})
}

func mustList(t *testing.T, ctx context.Context, s agent.Session, limit int) []agent.SessionItem {
	t.Helper()
	items, err := s.List(ctx, limit)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return items
}

func mustAppend(t *testing.T, ctx context.Context, s agent.Session, messages []model.Message) []agent.SessionItem {
	t.Helper()
	items, err := s.Append(ctx, messages)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return items
}

func mustReplace(t *testing.T, ctx context.Context, s agent.Session, firstID, lastID string, with []model.Message) []agent.SessionItem {
	t.Helper()
	items, err := s.Replace(ctx, firstID, lastID, with)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return items
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
		assertItemContents(t, mustList(t, ctx, s, 0))
		if err := s.Clear(ctx); err != nil {
			t.Fatalf("Clear on missing file: %v", err)
		}
	})

	t.Run("history survives a new instance in versioned item JSONL", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "session.jsonl")
		first := NewFile(path)
		appended := mustAppend(t, ctx, first, rangeMsgs(3))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read persisted JSONL: %v", err)
		}
		if !strings.Contains(string(data), `"version":2`) || !strings.Contains(string(data), appended[0].ID) {
			t.Fatalf("expected versioned item JSONL, got %s", data)
		}
		second := NewFile(path)
		got := mustList(t, ctx, second, 0)
		assertItemContents(t, got, "msg-1", "msg-2", "msg-3")
		if got[0].ID != appended[0].ID || got[2].ID != appended[2].ID {
			t.Fatalf("item IDs did not survive reopen: got %+v, want %+v", got, appended)
		}
	})

	t.Run("bad JSON line reports the line number", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "broken.jsonl")
		s := NewFile(path)
		mustAppend(t, ctx, s, rangeMsgs(2))
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := file.WriteString("this is not json\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = file.Close()

		_, err = s.List(ctx, 0)
		if err == nil || !strings.Contains(err.Error(), "line 3") {
			t.Fatalf("expected line 3 error, got: %v", err)
		}
	})

	t.Run("legacy v1 JSONL requires explicit migration", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "legacy.jsonl")
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"legacy"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := NewFile(path)
		for _, operation := range []func() error{
			func() error { _, err := s.List(ctx, 0); return err },
			func() error { _, err := s.Append(ctx, []model.Message{userMsg("new")}); return err },
			func() error { return s.Clear(ctx) },
		} {
			err := operation()
			var formatErr *FormatUpgradeError
			if !errors.As(err, &formatErr) || !strings.Contains(err.Error(), "format upgrade required") {
				t.Fatalf("legacy file error = %v, want clear format upgrade error", err)
			}
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
		mustAppend(t, ctx, a, rangeMsgs(3))
		assertItemContents(t, mustList(t, ctx, b, 0))
		mustAppend(t, ctx, b, []model.Message{userMsg("beta-1")})
		assertItemContents(t, mustList(t, ctx, a, 0), "msg-1", "msg-2", "msg-3")
		assertItemContents(t, mustList(t, ctx, b, 0), "beta-1")
	})

	t.Run("history and item IDs survive reopen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sessions.db")
		first, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		appended := mustAppend(t, context.Background(), first.Get("durable"), rangeMsgs(3))
		if err := first.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		second, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer func() { _ = second.Close() }()
		got := mustList(t, context.Background(), second.Get("durable"), 0)
		assertItemContents(t, got, "msg-1", "msg-2", "msg-3")
		if got[0].ID != appended[0].ID || got[2].ID != appended[2].ID {
			t.Fatalf("item IDs did not survive reopen: got %+v, want %+v", got, appended)
		}
	})

	t.Run("clear only touches its own session", func(t *testing.T) {
		store := openStore(t)
		ctx := context.Background()
		a, b := store.Get("alpha"), store.Get("beta")
		mustAppend(t, ctx, a, rangeMsgs(2))
		mustAppend(t, ctx, b, rangeMsgs(2))
		if err := a.Clear(ctx); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		assertItemContents(t, mustList(t, ctx, a, 0))
		assertItemContents(t, mustList(t, ctx, b, 0), "msg-1", "msg-2")
	})

	t.Run("concurrent appenders keep every message", func(t *testing.T) {
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
					if _, err := s.Append(ctx, []model.Message{userMsg(fmt.Sprintf("w%d-i%d", w, i))}); err != nil {
						errs <- err
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent Append: %v", err)
		}

		got := mustList(t, ctx, s, 0)
		if len(got) != workers*per {
			t.Fatalf("got %d messages, want %d", len(got), workers*per)
		}
		seen := map[string]bool{}
		for _, item := range got {
			seen[item.Message.Content] = true
		}
		if len(seen) != workers*per {
			t.Fatalf("duplicated messages: %d unique of %d", len(seen), workers*per)
		}
	})

	t.Run("legacy v1 schema requires explicit migration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.db")
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE session_items (
			session_id TEXT NOT NULL,
			idx INTEGER NOT NULL,
			message TEXT NOT NULL,
			PRIMARY KEY (session_id, idx)
		)`)
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewSQLiteStore(path)
		var formatErr *FormatUpgradeError
		if !errors.As(err, &formatErr) || !strings.Contains(err.Error(), "format upgrade required") {
			t.Fatalf("legacy sqlite error = %v, want clear format upgrade error", err)
		}
	})
}

func TestPersistentSessionRollback(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			var s agent.Session
			var reopen func() agent.Session
			if kind == "file" {
				path := filepath.Join(t.TempDir(), "history.jsonl")
				s = NewFile(path)
				reopen = func() agent.Session { return NewFile(path) }
			} else {
				store := openStore(t)
				s = store.Get("history")
				reopen = func() agent.Session { return store.Get("history") }
			}
			items := mustAppend(t, t.Context(), s, rangeMsgs(3))
			invalid := model.Message{Role: model.RoleUser, Parts: []model.ContentPart{{Extra: json.RawMessage(`{`)}}}
			if _, err := s.Append(t.Context(), []model.Message{userMsg("partial"), invalid}); err == nil {
				t.Fatal("invalid append succeeded")
			}
			if _, err := s.Replace(t.Context(), items[0].ID, items[2].ID, []model.Message{userMsg("partial"), invalid}); err == nil {
				t.Fatal("invalid replacement succeeded")
			}
			got := mustList(t, t.Context(), reopen(), 0)
			assertItemContents(t, got, "msg-1", "msg-2", "msg-3")
			for i := range items {
				if got[i].ID != items[i].ID {
					t.Fatal("failed operation changed storage IDs")
				}
			}
			replacement := mustReplace(t, t.Context(), s, items[0].ID, items[1].ID, []model.Message{assistantMsg("summary")})
			got = mustList(t, t.Context(), reopen(), 0)
			assertItemContents(t, got, "summary", "msg-3")
			if got[0].ID != replacement[0].ID || got[1].ID != items[2].ID {
				t.Fatal("replacement IDs did not persist")
			}
		})
	}
}

func TestSQLiteReplaceWriteFailureRollsBack(t *testing.T) {
	store := openStore(t)
	s := store.Get("history")
	items := mustAppend(t, t.Context(), s, rangeMsgs(3))
	_, err := store.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON session_items
		WHEN NEW.idx = 1 BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), items[0].ID, items[2].ID, rangeMsgs(2)); err == nil {
		t.Fatal("expected write failure")
	}
	got := mustList(t, t.Context(), s, 0)
	assertItemContents(t, got, "msg-1", "msg-2", "msg-3")
	for i := range items {
		if got[i].ID != items[i].ID {
			t.Fatal("transaction did not roll back")
		}
	}
}

func TestSQLiteMultipleStoresSerializeWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	var stores []*SQLiteStore
	for range 4 {
		store, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		stores = append(stores, store)
	}
	errs := make(chan error, len(stores))
	for _, store := range stores {
		go func() {
			for range 10 {
				if _, err := store.Get("shared").Append(t.Context(), rangeMsgs(1)); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for range stores {
		if err := <-errs; err != nil {
			t.Errorf("concurrent store write: %v", err)
		}
	}
	if got := mustList(t, t.Context(), stores[0].Get("shared"), 0); len(got) != 40 {
		t.Fatalf("stored %d items, want 40", len(got))
	}
}

func TestFileRejectsDuplicateIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := NewFile(path)
	mustAppend(t, t.Context(), s, rangeMsgs(1))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, data...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "duplicate item ID") {
		t.Fatalf("duplicate IDs: %v", err)
	}
}

func TestFileAtomicWriteCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	s := NewFile(path)
	items := mustAppend(t, t.Context(), s, rangeMsgs(2))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.rewriteItems(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("canceled write changed original: %v", err)
	}
	blocked := filepath.Join(dir, "directory")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := NewFile(blocked).rewriteItems(t.Context(), items); err == nil {
		t.Fatal("rename over directory unexpectedly succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("temporary files leaked: %+v", entries)
	}
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
	a := &agent.Agent{ID: "chatter", Name: "chatter", Instructions: "You are helpful.", Model: m}
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

	assertMessageContents(t, secondPrompt, "You are helpful.", "hello", "first answer", "again")
	if secondPrompt[0].Role != model.RoleSystem {
		t.Fatalf("first message role = %q, want system", secondPrompt[0].Role)
	}
	got := mustList(t, ctx, sess, 0)
	assertItemContents(t, got, "hello", "first answer", "again", "second answer")
	if got[0].Message.Role != model.RoleUser || got[1].Message.Role != model.RoleAssistant {
		t.Fatalf("unexpected roles: %s / %s", got[0].Message.Role, got[1].Message.Role)
	}
}

func TestRunWithSessionFailedRunNotPersisted(t *testing.T) {
	ctx := context.Background()
	a := &agent.Agent{ID: "session", Name: "session", Model: testutil.NewScripted(testutil.HTTPErrorStep(500, "boom"))}
	sess := NewInMemory()
	if _, err := agent.NewRunner().RunWithSession(ctx, a, sess, "hello"); err == nil {
		t.Fatal("expected run failure")
	}
	if got := mustList(t, ctx, sess, 0); len(got) != 0 {
		t.Fatalf("failed run persisted %d messages", len(got))
	}
}

func TestRunStreamWithSession(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewScriptedStream(
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("hi")}, FinishReason: "stop"},
		testutil.StreamStep{Deltas: []model.StreamEvent{testutil.TextChunk("again!")}, FinishReason: "stop"},
	)
	a := &agent.Agent{ID: "streamer", Name: "streamer", Model: m}
	sess := NewInMemory()
	if _, err := agent.NewRunner().RunStreamWithSession(ctx, a, sess, "hello").Wait(); err != nil {
		t.Fatalf("stream run 1: %v", err)
	}
	if _, err := agent.NewRunner().RunStreamWithSession(ctx, a, sess, "more").Wait(); err != nil {
		t.Fatalf("stream run 2: %v", err)
	}
	assertMessageContents(t, m.LastRequest().Messages, "hello", "hi", "more")
	assertItemContents(t, mustList(t, ctx, sess, 0), "hello", "hi", "more", "again!")
}

type failingSession struct {
	listErr   error
	appendErr error
}

func (f *failingSession) List(ctx context.Context, limit int) ([]agent.SessionItem, error) {
	return nil, f.listErr
}

func (f *failingSession) Append(ctx context.Context, messages []model.Message) ([]agent.SessionItem, error) {
	return nil, f.appendErr
}

func (f *failingSession) Replace(ctx context.Context, firstID, lastID string, with []model.Message) ([]agent.SessionItem, error) {
	return nil, nil
}

func (f *failingSession) Clear(ctx context.Context) error { return nil }

func TestRunStreamWithSessionLoadError(t *testing.T) {
	sess := &failingSession{listErr: errors.New("disk gone")}
	r := agent.NewRunner().RunStreamWithSession(context.Background(),
		&agent.Agent{ID: "session", Name: "session", Model: testutil.NewScripted()}, sess, "hello")
	res, err := r.Wait()
	if err == nil {
		t.Fatal("expected load error")
	}
	if res != nil || !strings.Contains(err.Error(), "load session") {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestRunWithSessionCompressor(t *testing.T) {
	ctx := context.Background()
	sess := NewInMemory()
	mustAppend(t, ctx, sess, rangeMsgs(6))
	m := testutil.NewScripted(testutil.TextStep("done"))
	r := agent.NewRunner()
	r.Compressor = NewSlidingWindow(2)
	res, err := r.RunWithSession(ctx, &agent.Agent{ID: "session", Name: "session", Model: m}, sess, "question")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertMessageContents(t, m.LastRequest().Messages, "msg-5", "msg-6", "question")
	assertMessageContents(t, res.Messages, "msg-5", "msg-6", "question", "done")
	assertItemContents(t, mustList(t, ctx, sess, 0),
		"msg-1", "msg-2", "msg-3", "msg-4", "msg-5", "msg-6", "question", "done")
}

func TestRunWithSessionAppendError(t *testing.T) {
	ctx := context.Background()
	sess := &failingSession{appendErr: errors.New("disk full")}
	_, err := agent.NewRunner().RunWithSession(ctx,
		&agent.Agent{ID: "session", Name: "session", Model: testutil.NewScripted(testutil.TextStep("fine"))}, sess, "hello")
	if err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("expected writeback failure, got %v", err)
	}
}

func TestRunWithSessionNilSession(t *testing.T) {
	_, err := agent.NewRunner().RunWithSession(context.Background(),
		&agent.Agent{ID: "session", Name: "session", Model: testutil.NewScripted(testutil.TextStep("never"))}, nil, "hi")
	if err == nil || !strings.Contains(err.Error(), "session is nil") {
		t.Fatalf("expected nil session error, got %v", err)
	}
}
