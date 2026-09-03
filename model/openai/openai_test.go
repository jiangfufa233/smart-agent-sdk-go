package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/agent-sdk/model"
)

// newTestClient builds a client against a local fake endpoint.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return New(Config{APIKey: "sk-test", BaseURL: ts.URL, DefaultModel: "gpt-test"})
}

func okBody() string {
	return `{"id":"resp_1","model":"gpt-test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`
}

func TestWireFormat(t *testing.T) {
	temp := 0.3
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("bad json: %v", err)
		}
		if got["model"] != "gpt-test" {
			t.Errorf("default model not applied: %v", got["model"])
		}
		if _, ok := got["settings"]; ok {
			t.Errorf("settings must be flattened onto the request: %s", body)
		}
		if got["temperature"] != 0.3 {
			t.Errorf("temperature missing: %s", body)
		}
		tc, _ := got["tool_choice"].(map[string]any)
		if tc == nil || tc["type"] != "function" {
			t.Errorf("tool_choice wrong: %v", got["tool_choice"])
		}
		rf, _ := got["response_format"].(map[string]any)
		if rf == nil || rf["type"] != "json_object" {
			t.Errorf("response_format wrong: %v", got["response_format"])
		}
		msgs, _ := got["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		parts, _ := first["content"].([]any)
		if len(parts) != 2 {
			t.Errorf("multimodal content not serialized as array: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody()))
	})

	req := &model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.ContentPart{
			{Type: model.PartText, Text: "see"},
			{Type: model.PartImageURL, ImageURL: &model.ImageURL{URL: "https://x/y.png"}},
		}}},
		Settings: model.Settings{
			Temperature:    &temp,
			ToolChoice:     model.ToolChoiceFunction("f"),
			ResponseFormat: &model.ResponseFormat{Type: model.FormatJSONObject},
		},
	}
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestAuthHeader(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(okBody()))
	})
	_, _ = c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
}

func TestSuccessParse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody()))
	})
	res, err := c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "resp_1" || res.Model != "gpt-test" {
		t.Fatalf("identity fields wrong: %+v", res)
	}
	if res.Message.Content != "hello" || res.FinishReason != "stop" {
		t.Fatalf("message wrong: %+v", res)
	}
	if res.Usage.PromptTokens != 5 || res.Usage.PromptTokensDetails.CachedTokens != 3 {
		t.Fatalf("usage wrong: %+v", res.Usage)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status    int
		kind      model.ErrorKind
		retryable bool
	}{
		{401, model.ErrorAuth, false},
		{429, model.ErrorRateLimited, true},
		{500, model.ErrorServerError, true},
		{400, model.ErrorInvalidRequest, false},
	}
	for _, tc := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
		})
		_, err := c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
		var me *model.ModelError
		if !errors.As(err, &me) {
			t.Fatalf("status %d: expected *ModelError, got %v", tc.status, err)
		}
		if me.Kind != tc.kind || me.Retryable != tc.retryable || me.StatusCode != tc.status {
			t.Fatalf("status %d: got (%s,%v), want (%s,%v)", tc.status, me.Kind, me.Retryable, tc.kind, tc.retryable)
		}
	}
}

func TestProtocolErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	_, err := c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorProtocol || me.Retryable {
		t.Fatalf("expected non-retryable protocol error, got %v", err)
	}

	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	_, err = c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if !errors.As(err, &me) || me.Kind != model.ErrorProtocol {
		t.Fatalf("empty choices should be a protocol error, got %v", err)
	}
}

func TestContextCanceledPassesThrough(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
		_, _ = w.Write([]byte(okBody()))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Chat(ctx, &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorTimeout || !me.Retryable {
		t.Fatalf("deadline should be retryable timeout: %v", err)
	}
}

func TestNoModelError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	c := New(Config{BaseURL: ts.URL}) // no DefaultModel
	_, err := c.Chat(context.Background(), &model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "x"}}})
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrorInvalidRequest {
		t.Fatalf("expected invalid_request for missing model, got %v", err)
	}
}
