// Command chat demonstrates a multi-turn terminal conversation with an agent.
//
// LLM settings are read from ~/.config/opencode/opencode.jsonc (baseURL,
// apiKey and default model of the first provider). They can be overridden
// with the LLM_BASE_URL, LLM_API_KEY and LLM_MODEL environment variables.
//
// Usage:
//
//	go run ./examples/chat
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangfufa233/openai-agent-sdk-go/agent"
	"github.com/jiangfufa233/openai-agent-sdk-go/model"
	"github.com/jiangfufa233/openai-agent-sdk-go/model/openai"
)

// llmConfig holds the resolved endpoint settings for the chat model.
type llmConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

func main() {
	cfg := loadLLMConfig()
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "no apiKey found: set LLM_API_KEY or fill in ~/.config/opencode/opencode.jsonc")
		os.Exit(1)
	}

	a := &agent.Agent{
		Name:         "assistant",
		Instructions: "You are a helpful assistant.",
		Model: openai.New(openai.Config{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
		}),
		ModelName: cfg.Model,
	}
	runner := agent.NewRunner()

	fmt.Printf("Chat with the agent [%s @ %s] (Ctrl-D to quit):\n", cfg.Model, cfg.BaseURL)
	var history []model.Message
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break
		}
		input := scanner.Text()
		if input == "" {
			continue
		}
		run := runner.RunStreamWithHistory(context.Background(), a, history, input)
		fmt.Print("agent> ")
		for ev := range run.Events {
			switch ev.Type {
			case agent.StreamTextDelta:
				fmt.Print(ev.Text)
			case agent.StreamRunError:
				fmt.Fprintln(os.Stderr, "error:", ev.Err)
			}
		}
		fmt.Println()
		res, err := run.Result()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		history = res.Messages
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read error:", err)
	}
}

// loadLLMConfig resolves settings from opencode.jsonc, then applies env-var
// overrides (LLM_BASE_URL, LLM_API_KEY, LLM_MODEL).
func loadLLMConfig() llmConfig {
	var cfg llmConfig
	if home, err := os.UserHomeDir(); err == nil {
		cfg = parseOpenCodeConfig(filepath.Join(home, ".config", "opencode", "opencode.jsonc"))
	}
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.Model = v
	}
	return cfg
}

// parseOpenCodeConfig extracts baseURL/apiKey and the default model from an
// opencode.jsonc file. The file may contain // and /* */ comments, which are
// stripped before JSON decoding.
func parseOpenCodeConfig(path string) llmConfig {
	var cfg llmConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var doc struct {
		Model    string `json:"model"`
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &doc); err != nil {
		fmt.Fprintln(os.Stderr, "warning: parse", path, ":", err)
		return cfg
	}
	if strings.Contains(doc.Model, "/") {
		cfg.Model = doc.Model[strings.LastIndex(doc.Model, "/")+1:]
	}
	for _, p := range doc.Provider {
		cfg.BaseURL = p.Options.BaseURL
		cfg.APIKey = p.Options.APIKey
		break
	}
	return cfg
}

// stripJSONComments removes // and /* */ comments outside of JSON strings.
func stripJSONComments(b []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '/':
			if i+1 < len(b) && b[i+1] == '/' {
				for i < len(b) && b[i] != '\n' {
					i++
				}
				if i < len(b) {
					out = append(out, '\n')
				}
			} else if i+1 < len(b) && b[i+1] == '*' {
				i += 2
				for i+1 < len(b) && (b[i] != '*' || b[i+1] != '/') {
					i++
				}
				i++ // skip the closing '/'
			} else {
				out = append(out, c)
			}
		default:
			out = append(out, c)
		}
	}
	return out
}
