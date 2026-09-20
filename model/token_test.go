package model

import (
	"errors"
	"reflect"
	"testing"
)

func TestUnsupportedTokenModelError(t *testing.T) {
	for _, modelName := range []string{"", "gpt-4o-mini", "provider-model"} {
		err := &UnsupportedTokenModelError{ModelName: modelName}
		assertUnsupportedTokenModel(t, 0, err, modelName)
		if err.Error() == "" || errors.Is(err, errors.New("other")) {
			t.Fatalf("invalid unsupported error: %v", err)
		}
	}
}

func TestTokenCounterAcceptsProviderImplementation(t *testing.T) {
	counter := &recordingTokenCounter{}
	var injected TokenCounter = counter

	textTokens, err := injected.CountText("provider-model", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if textTokens != 5 {
		t.Fatalf("text tokens = %d, want 5", textTokens)
	}
	if counter.textModel != "provider-model" || counter.text != "hello" {
		t.Fatalf("text call = model %q, text %q", counter.textModel, counter.text)
	}

	messages := []Message{
		{Role: RoleUser, Content: "Use a tool."},
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: FunctionCall{Name: "lookup", Arguments: `{"query":"q"}`},
			}},
		},
		{Role: RoleTool, ToolCallID: "call_1", Content: "result"},
		{
			Role: RoleUser,
			Parts: []ContentPart{
				{Type: PartText, Text: "And this?"},
				{Type: PartImageURL, ImageURL: &ImageURL{URL: "https://example.test/image.png"}},
			},
		},
	}
	messageTokens, err := injected.CountMessages("provider-model", messages)
	if err != nil {
		t.Fatal(err)
	}
	if messageTokens != 23 {
		t.Fatalf("message tokens = %d, want 23", messageTokens)
	}
	if counter.messageModel != "provider-model" {
		t.Fatalf("message model = %q, want provider-model", counter.messageModel)
	}
	if !reflect.DeepEqual(counter.messages, messages) {
		t.Fatalf("messages were not passed through intact:\n got: %#v\nwant: %#v", counter.messages, messages)
	}
}

func assertUnsupportedTokenModel(t *testing.T, tokens int, err error, modelName string) {
	t.Helper()
	if tokens != 0 {
		t.Fatalf("tokens = %d, want 0 when counting is unsupported", tokens)
	}
	if !errors.Is(err, ErrUnsupportedTokenModel) {
		t.Fatalf("expected ErrUnsupportedTokenModel, got %v", err)
	}
	var unsupported *UnsupportedTokenModelError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected UnsupportedTokenModelError, got %T", err)
	}
	if unsupported.ModelName != modelName {
		t.Fatalf("error model = %q, want %q", unsupported.ModelName, modelName)
	}
}

type recordingTokenCounter struct {
	textModel    string
	text         string
	messageModel string
	messages     []Message
}

var _ TokenCounter = (*recordingTokenCounter)(nil)

func (c *recordingTokenCounter) CountText(modelName, text string) (int, error) {
	c.textModel, c.text = modelName, text
	return 5, nil
}

func (c *recordingTokenCounter) CountMessages(modelName string, messages []Message) (int, error) {
	c.messageModel = modelName
	c.messages = append([]Message(nil), messages...)
	return 23, nil
}
