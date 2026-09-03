package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalString(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleUser, Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"role":"user","content":"hi"}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestMessageMarshalOmitsEmptyAssistantContent(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{
		ID: "1", Type: "function", Function: FunctionCall{Name: "f", Arguments: "{}"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"content"`) {
		t.Fatalf("assistant tool_call message should omit content: %s", b)
	}
}

func TestMessageMarshalKeepsEmptyToolContent(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleTool, ToolCallID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":""`) {
		t.Fatalf("tool messages must always carry content: %s", b)
	}
}

func TestMessageMarshalParts(t *testing.T) {
	m := Message{Role: RoleUser, Parts: []ContentPart{
		{Type: PartText, Text: "look"},
		{Type: PartImageURL, ImageURL: &ImageURL{URL: "https://x/y.png"}},
	}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Content) != 2 {
		t.Fatalf("expected 2 parts, got %s", b)
	}
	if w.Content[0]["text"] != "look" {
		t.Errorf("part 0 = %v", w.Content[0])
	}
	img := w.Content[1]["image_url"].(map[string]any)
	if img["url"] != "https://x/y.png" {
		t.Errorf("part 1 = %v", w.Content[1])
	}
}

func TestMessageUnmarshalStringAndParts(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "hi" || len(m.Parts) != 0 {
		t.Fatalf("string content: %+v", m)
	}

	raw := `{"role":"user","content":[
		{"type":"text","text":"a"},
		{"type":"input_audio","data":"x","format":"wav"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "" || len(m.Parts) != 2 {
		t.Fatalf("parts content: %+v", m)
	}
	if m.Parts[1].Type != "input_audio" || len(m.Parts[1].Extra) == 0 {
		t.Fatalf("unknown part must be preserved via Extra: %+v", m.Parts[1])
	}

	// Round-trip must keep the provider-specific part verbatim.
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Parts[1].Extra) != string(m.Parts[1].Extra) {
		t.Fatalf("extra part not round-tripped: %s vs %s", back.Parts[1].Extra, m.Parts[1].Extra)
	}
}

func TestToolChoiceMarshal(t *testing.T) {
	cases := map[string]struct {
		in   *ToolChoice
		want string
	}{
		"auto":     {ToolChoiceAuto(), `"auto"`},
		"none":     {ToolChoiceNone(), `"none"`},
		"required": {ToolChoiceRequired(), `"required"`},
		"function": {ToolChoiceFunction("f"), `{"function":{"name":"f"},"type":"function"}`},
	}
	for name, tc := range cases {
		b, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(b) != tc.want {
			t.Errorf("%s: got %s, want %s", name, b, tc.want)
		}
	}
}

func TestRequestSettingsFlatten(t *testing.T) {
	temp := 0.5
	req := Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
		Settings: Settings{Temperature: &temp, MaxTokens: 10},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["settings"]; ok {
		t.Fatalf("settings must be flattened onto the request, not nested: %s", b)
	}
	if m["temperature"] != 0.5 || m["max_tokens"] != 10.0 {
		t.Fatalf("flat settings missing: %s", b)
	}
}

func TestUsageAccumulate(t *testing.T) {
	var u Usage
	u.Accumulate(Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		PromptTokensDetails: &TokenDetails{CachedTokens: 4}})
	u.Accumulate(Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		PromptTokensDetails: &TokenDetails{CachedTokens: 6}})

	if u.PromptTokens != 11 || u.CompletionTokens != 7 || u.TotalTokens != 18 {
		t.Fatalf("totals wrong: %+v", u)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 10 {
		t.Fatalf("cached tokens wrong: %+v", u.PromptTokensDetails)
	}
}
