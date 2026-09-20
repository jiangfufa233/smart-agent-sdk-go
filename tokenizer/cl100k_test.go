package tokenizer_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tokenizer"
)

func TestCL100KTextVectors(t *testing.T) {
	c, err := tokenizer.NewCL100K()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0},
		{"hello world", 2},
		{"antidisestablishmentarianism", 6},
		{"2 + 2 = 4", 7},
		{"お誕生日おめでとう", 9},
	} {
		for _, name := range []string{"cl100k_base", "gpt-4-0613", "gpt-3.5-turbo-0125"} {
			got, err := c.CountText(name, tc.text)
			if err != nil || got != tc.want {
				t.Errorf("CountText(%q, %q) = %d, %v; want %d", name, tc.text, got, err, tc.want)
			}
		}
	}
}

func TestCL100KOfficialReference(t *testing.T) {
	data, err := os.ReadFile("testdata/cl100k.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reference string `json:"reference"`
		Vectors   []struct {
			Text   string `json:"text"`
			Tokens int    `json:"tokens"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Reference == "" || len(fixture.Vectors) < 800 {
		t.Fatal("incomplete reference corpus")
	}
	c, err := tokenizer.NewCL100K()
	if err != nil {
		t.Fatal(err)
	}
	for i, vector := range fixture.Vectors {
		got, err := c.CountText("cl100k_base", vector.Text)
		if err != nil || got != vector.Tokens {
			t.Errorf("vector %d (%q): %d, %v; reference %d", i, vector.Text, got, err, vector.Tokens)
		}
	}
}

func TestCL100KMessageFraming(t *testing.T) {
	c, err := tokenizer.NewCL100K()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		messages []model.Message
		want     int
	}{
		{"gpt-4-0613", nil, 3},
		{"gpt-4-0613", []model.Message{{Role: model.RoleUser, Content: "hello world"}}, 9},
		{"gpt-4-0613", []model.Message{{Role: model.RoleUser, Name: "bob", Content: "hello world"}}, 11},
		{"gpt-3.5-turbo-0301", []model.Message{{Role: model.RoleUser, Name: "bob", Content: "hello world"}}, 10},
	} {
		got, err := c.CountMessages(tc.name, tc.messages)
		if err != nil || got != tc.want {
			t.Errorf("CountMessages(%q, %+v) = %d, %v; want %d", tc.name, tc.messages, got, err, tc.want)
		}
	}
}

func TestCL100KStructuredMessageVectors(t *testing.T) {
	c, err := tokenizer.NewCL100K()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		messages []model.Message
		want     int
	}{
		{"tool conversation", []model.Message{
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call_1", Type: "function", Function: model.FunctionCall{Name: "weather", Arguments: `{"city":"Tokyo"}`}}}},
			{Role: model.RoleTool, ToolCallID: "call_1", Content: `{"temperature_c":18}`},
		}, 34},
		{"text and image", []model.Message{{Role: model.RoleUser, Content: "not sent", Parts: []model.ContentPart{
			{Type: model.PartText, Text: "Describe this image."},
			{Type: model.PartImageURL, ImageURL: &model.ImageURL{URL: "https://example.test/image.png", Detail: "high"}},
		}}}, 27},
		{"nil image", []model.Message{{Role: model.RoleUser, Parts: []model.ContentPart{{Type: model.PartImageURL}}}}, 13},
		{"provider raw JSON", []model.Message{{Role: model.RoleUser, Parts: []model.ContentPart{{Type: model.PartText, Text: "not sent", Extra: []byte(`{ "type": "audio", "data": "aGVsbG8=" }`)}}}}, 24},
		{"tool index", []model.Message{{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "call_1", Type: "function", Index: 2, Function: model.FunctionCall{Name: "weather", Arguments: `{"city":"Tokyo"}`}}}}}, 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.CountMessages("gpt-4-0613", tc.messages)
			if err != nil || got != tc.want {
				t.Fatalf("structured estimate: %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestCL100KRejectsUnsupportedAndInvalidInput(t *testing.T) {
	c, err := tokenizer.NewCL100K()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "gpt-4o", "gpt-4-future", "private-model"} {
		for _, count := range []func() (int, error){
			func() (int, error) { return c.CountText(name, "hello") },
			func() (int, error) { return c.CountMessages(name, nil) },
		} {
			got, err := count()
			var unsupported *model.UnsupportedTokenModelError
			if got != 0 || !errors.Is(err, model.ErrUnsupportedTokenModel) || !errors.As(err, &unsupported) || unsupported.ModelName != name {
				t.Fatalf("unsupported %q = %d, %v", name, got, err)
			}
		}
	}
	if _, err := c.CountMessages("cl100k_base", nil); !errors.Is(err, model.ErrUnsupportedTokenModel) {
		t.Fatalf("encoding name must not invent message framing: %v", err)
	}
	if _, err := c.CountText("cl100k_base", string([]byte{0xff})); !errors.Is(err, tokenizer.ErrInvalidUTF8) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
	for _, message := range []model.Message{
		{Role: model.RoleUser, Content: string([]byte{0xff})},
		{Role: model.RoleUser, Parts: []model.ContentPart{{Type: "unknown"}}},
		{Role: model.RoleUser, Parts: []model.ContentPart{{Extra: []byte(`{`)}}},
	} {
		if _, err := c.CountMessages("gpt-4-0613", []model.Message{message}); err == nil {
			t.Fatalf("invalid message accepted: %+v", message)
		}
	}
}

func TestCL100KConcurrentCounters(t *testing.T) {
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			c, err := tokenizer.NewCL100K()
			if err != nil {
				t.Error(err)
				return
			}
			for range 50 {
				if n, err := c.CountText("cl100k_base", "お誕生日おめでとう"); err != nil || n != 9 {
					t.Errorf("concurrent count: %d, %v", n, err)
				}
			}
		})
	}
	wg.Wait()
}

type noNetwork struct{}

func (noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	panic("tokenizer attempted an HTTP request")
}

func TestOfflineStartup(t *testing.T) {
	if os.Getenv("TOKENIZER_OFFLINE_TEST") == "1" {
		http.DefaultTransport = noNetwork{}
		c, err := tokenizer.NewCL100K()
		if err != nil {
			t.Fatal(err)
		}
		if n, err := c.CountText("cl100k_base", "hello world"); n != 2 || err != nil {
			t.Fatalf("offline counter: %d, %v", n, err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOfflineStartup$")
	cmd.Env = append(os.Environ(), "TOKENIZER_OFFLINE_TEST=1", fmt.Sprintf("TIKTOKEN_CACHE_DIR=%s", t.TempDir()))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh offline process: %v\n%s", err, output)
	}
}
