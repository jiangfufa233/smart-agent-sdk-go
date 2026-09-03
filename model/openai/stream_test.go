package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/example/agent-sdk/model"
)

// sseServer returns a test server that streams the given raw SSE text with
// flushing between chunks.
func sseServer(t *testing.T, chunks ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprint(w, c)
			flusher.Flush()
		}
	}))
}

func chunkJSON(id, deltaJSON, finish string) string {
	return "data: " + fmt.Sprintf(
		`{"id":%q,"model":"gpt-test","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`+"\n\n",
		id, deltaJSON, orNull(finish))
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return strconv.Quote(s)
}

func drainStream(t *testing.T, sr model.StreamReader) []model.StreamEvent {
	t.Helper()
	defer sr.Close()
	var evs []model.StreamEvent
	for sr.Next() {
		evs = append(evs, sr.Event())
	}
	return evs
}

func requireEvents(t *testing.T, got []model.StreamEvent, want []model.StreamEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Type != want[i].Type {
			t.Errorf("event %d type = %s, want %s", i, got[i].Type, want[i].Type)
			continue
		}
		switch want[i].Type {
		case model.StreamTextDelta:
			if got[i].Text != want[i].Text {
				t.Errorf("event %d text = %q, want %q", i, got[i].Text, want[i].Text)
			}
		case model.StreamToolCallDelta:
			if got[i].ToolCall != want[i].ToolCall {
				t.Errorf("event %d tool call = %+v, want %+v", i, got[i].ToolCall, want[i].ToolCall)
			}
		case model.StreamFinish:
			g, w := got[i], want[i]
			if g.FinishReason != w.FinishReason || g.Usage != w.Usage || g.ID != w.ID {
				t.Errorf("event %d finish = %+v, want %+v", i, g, w)
			}
		}
	}
}

func TestChatStreamTextDeltas(t *testing.T) {
	ts := sseServer(t,
		chunkJSON("cmpl-1", `{"role":"assistant"}`, ""), // role-only: no event
		chunkJSON("cmpl-1", `{"content":"Hel"}`, ""),
		chunkJSON("cmpl-1", `{"content":"lo"}`, ""),
		chunkJSON("cmpl-1", `{}`, "stop"),
		"data: "+`{"id":"cmpl-1","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`+"\n\n",
		"data: [DONE]\n\n",
	)
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})

	sr, err := c.ChatStream(context.Background(), &model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := drainStream(t, sr)
	requireEvents(t, evs, []model.StreamEvent{
		{Type: model.StreamTextDelta, Text: "Hel"},
		{Type: model.StreamTextDelta, Text: "lo"},
		{Type: model.StreamFinish, FinishReason: "stop",
			Usage: model.Usage{PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13}, ID: "cmpl-1"},
	})
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
}

func TestChatStreamSendsStreamOptions(t *testing.T) {
	var seen map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Errorf("bad body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})
	if _, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if seen["stream"] != true {
		t.Errorf("stream flag missing: %v", seen)
	}
	so, _ := seen["stream_options"].(map[string]any)
	if so == nil || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage missing: %v", seen)
	}
}

func TestChatStreamDisableStreamUsage(t *testing.T) {
	var seen map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = map[string]any{}
		_ = json.Unmarshal(body, &seen)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test", DisableStreamUsage: true})
	if _, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["stream_options"]; ok {
		t.Errorf("stream_options should be omitted: %v", seen)
	}
}

func TestChatStreamToolCallDeltas(t *testing.T) {
	ts := sseServer(t,
		chunkJSON("cmpl-2", `{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}`, ""),
		chunkJSON("cmpl-2", `{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Bei"}}]}`, ""),
		chunkJSON("cmpl-2", `{"tool_calls":[{"index":0,"function":{"arguments":"jing\"}"}}]}`, ""),
		chunkJSON("cmpl-2", `{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"lookup","arguments":"{}"}}]}`, ""),
		chunkJSON("cmpl-2", `{}`, "tool_calls"),
		"data: [DONE]\n\n",
	)
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})

	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "weather?"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drainStream(t, sr)
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("got %d events, want 5: %+v", len(evs), evs)
	}
	requireEvents(t, evs[:4], []model.StreamEvent{
		{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{Index: 0, ID: "call_a", Type: "function", Function: model.FunctionCall{Name: "get_weather"}}},
		{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{Index: 0, Type: "function", Function: model.FunctionCall{Arguments: `{"city":"Bei`}}},
		{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{Index: 0, Type: "function", Function: model.FunctionCall{Arguments: `jing"}`}}},
		{Type: model.StreamToolCallDelta, ToolCall: model.ToolCall{Index: 1, ID: "call_b", Type: "function", Function: model.FunctionCall{Name: "lookup", Arguments: "{}"}}},
	})
	if evs[4].Type != model.StreamFinish || evs[4].FinishReason != "tool_calls" {
		t.Fatalf("last event = %+v", evs[4])
	}

	// The fragments must assemble into complete calls.
	var acc model.ToolCallAccumulator
	for _, ev := range evs {
		if ev.Type == model.StreamToolCallDelta {
			acc.Add(ev.ToolCall)
		}
	}
	calls := acc.Calls()
	if len(calls) != 2 ||
		calls[0].ID != "call_a" || calls[0].Function.Name != "get_weather" || calls[0].Function.Arguments != `{"city":"Beijing"}` ||
		calls[1].ID != "call_b" || calls[1].Function.Arguments != "{}" {
		t.Fatalf("assembled calls = %+v", calls)
	}
}

func TestChatStreamTruncatedIsProtocolError(t *testing.T) {
	ts := sseServer(t,
		chunkJSON("cmpl-3", `{"content":"partial"}`, ""),
		// connection closes without finish_reason / [DONE]
	)
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})
	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	n := 0
	for sr.Next() {
		n++
	}
	var me *model.ModelError
	if !errors.As(sr.Err(), &me) || me.Kind != model.ErrorProtocol || me.Retryable {
		t.Fatalf("Err = %v, want non-retryable protocol error after %d events", sr.Err(), n)
	}
}

func TestChatStreamEOFWithoutDONEIsClean(t *testing.T) {
	ts := sseServer(t,
		chunkJSON("cmpl-4", `{"content":"done soon"}`, ""),
		chunkJSON("cmpl-4", `{}`, "stop"),
		// some backends end without [DONE]
	)
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})
	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drainStream(t, sr)
	if err := sr.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
	if len(evs) != 2 || evs[1].Type != model.StreamFinish {
		t.Fatalf("got %+v", evs)
	}
}

func TestChatStreamBadChunkJSON(t *testing.T) {
	ts := sseServer(t, "data: {not json}\n\n")
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})
	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	for sr.Next() {
	}
	var me *model.ModelError
	if !errors.As(sr.Err(), &me) || me.Kind != model.ErrorProtocol || me.Body == "" {
		t.Fatalf("Err = %v, want protocol error carrying body", sr.Err())
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})
	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if sr != nil || err == nil {
		t.Fatalf("got (%v, %v), want nil reader and error", sr, err)
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorRateLimited || me.StatusCode != 429 {
		t.Fatalf("err = %v, want retryable rate_limited", err)
	}
}

func TestChatStreamContextCanceledMidStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, chunkJSON("cmpl-5", `{"content":"first"}`, ""))
		flusher.Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL, DefaultModel: "gpt-test"})

	ctx, cancel := context.WithCancel(context.Background())
	sr, err := c.ChatStream(ctx, &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	if !sr.Next() {
		t.Fatalf("first Next = false, Err = %v", sr.Err())
	}
	cancel()
	for sr.Next() {
	}
	if err := sr.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", err)
	}
}

func TestChatStreamMissingModel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL})
	sr, err := c.ChatStream(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if sr != nil {
		t.Fatal("expected nil reader")
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorInvalidRequest {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

func TestChatTransportErrorIsTyped(t *testing.T) {
	// A server that accepts the connection but never answers: the client's
	// HTTP timeout must surface as a retryable timeout ModelError, not a
	// bare error, so retry middleware can act on it.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer ts.Close()
	c := New(Config{
		BaseURL: ts.URL, DefaultModel: "gpt-test",
		HTTPClient: &http.Client{Timeout: 50 * time.Millisecond},
	})
	_, err := c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorTimeout || !me.Retryable {
		t.Fatalf("err = %v, want retryable timeout", err)
	}
}
