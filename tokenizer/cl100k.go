package tokenizer

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/hupe1980/go-tiktoken"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

var ErrInvalidUTF8 = errors.New("tokenizer: input is not valid UTF-8")

// CL100KCounter counts ordinary text and estimates messages, not billable Usage.
type CL100KCounter struct {
	encoding *tiktoken.Encoding
}

// NewCL100K loads the embedded vocabulary without network or disk-cache access.
func NewCL100K() (*CL100KCounter, error) {
	embeddedCL100K.once.Do(func() {
		embeddedCL100K.encoding, embeddedCL100K.err = tiktoken.NewEncodingByName(tiktoken.CL100kBase)
	})
	if embeddedCL100K.err != nil {
		return nil, embeddedCL100K.err
	}
	return &CL100KCounter{encoding: embeddedCL100K.encoding}, nil
}

var _ model.TokenCounter = (*CL100KCounter)(nil)

var embeddedCL100K struct {
	once     sync.Once
	encoding *tiktoken.Encoding
	err      error
}

type messageRules struct {
	tokensPerMessage int
	tokensPerName    int
}

// Exact names prevent future models from silently inheriting the wrong encoding.
var cl100kModels = map[string]messageRules{
	"gpt-3.5-turbo-0301":     {4, -1},
	"gpt-3.5-turbo-0613":     {3, 1},
	"gpt-3.5-turbo-1106":     {3, 1},
	"gpt-3.5-turbo-0125":     {3, 1},
	"gpt-3.5-turbo-16k-0613": {3, 1},
	"gpt-4-0314":             {3, 1},
	"gpt-4-32k-0314":         {3, 1},
	"gpt-4-0613":             {3, 1},
	"gpt-4-32k-0613":         {3, 1},
	"gpt-3.5-turbo":          {3, 1},
	"gpt-4":                  {3, 1},
}

// CountText also accepts "cl100k_base" to select the encoding without a model.
// Special-token-looking substrings are ordinary text, not control syntax.
func (c *CL100KCounter) CountText(modelName, text string) (int, error) {
	if _, ok := cl100kModels[modelName]; !ok && modelName != "cl100k_base" {
		return 0, &model.UnsupportedTokenModelError{ModelName: modelName}
	}
	return c.count(text)
}

func (c *CL100KCounter) CountMessages(modelName string, messages []model.Message) (int, error) {
	rules, ok := cl100kModels[modelName]
	if !ok {
		return 0, &model.UnsupportedTokenModelError{ModelName: modelName}
	}
	tokens := 3 // Assistant reply prefix in the documented Chat Completions framing.
	for _, message := range messages {
		tokens += rules.tokensPerMessage
		texts := []string{string(message.Role), message.ToolCallID}
		if message.Name != "" {
			tokens += rules.tokensPerName
			texts = append(texts, message.Name)
		}
		if len(message.Parts) == 0 {
			texts = append(texts, message.Content)
		}
		for _, part := range message.Parts {
			tokens += 3
			switch {
			case len(part.Extra) > 0:
				var compact bytes.Buffer
				if err := json.Compact(&compact, part.Extra); err != nil {
					return 0, err
				}
				texts = append(texts, compact.String())
			case part.Type == model.PartText:
				texts = append(texts, part.Type, part.Text)
			case part.Type == model.PartImageURL:
				texts = append(texts, part.Type)
				if part.ImageURL == nil {
					texts = append(texts, "null")
				} else {
					texts = append(texts, part.ImageURL.URL, part.ImageURL.Detail)
				}
			default:
				_, err := part.MarshalJSON()
				return 0, err
			}
		}
		for _, call := range message.ToolCalls {
			tokens += 3
			texts = append(texts, call.ID, call.Type, call.Function.Name, call.Function.Arguments)
			if call.Index != 0 {
				texts = append(texts, strconv.Itoa(call.Index))
			}
		}
		for _, text := range texts {
			n, err := c.count(text)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
	}
	return tokens, nil
}

func (c *CL100KCounter) count(text string) (int, error) {
	if !utf8.ValidString(text) {
		return 0, ErrInvalidUTF8
	}
	tokens, _ := c.encoding.EncodeOrdinary(text)
	return len(tokens), nil
}
