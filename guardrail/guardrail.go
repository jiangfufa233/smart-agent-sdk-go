// Package guardrail provides ready-to-use input guardrails.
//
// Guardrails are plain functions, so anything can be a guardrail. The
// built-ins here cover static checks; for model-based ones (e.g.
// moderation), implement agent.InputGuardrail with a model client wrapped in
// model.WithTimeout so a hung guardrail cannot stall the run:
//
//	g := agent.InputGuardrail{
//	    Name: "moderation",
//	    Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
//	        resp, err := moderator.Chat(ctx, &model.Request{
//	            Messages: []model.Message{
//	                {Role: model.RoleSystem, Content: "Flag unsafe user input. Reply JSON {\"unsafe\": bool, \"reason\": string}."},
//	                {Role: model.RoleUser, Content: input},
//	            },
//	        })
//	        if err != nil {
//	            return agent.GuardrailResult{}, err // fail-closed
//	        }
//	        var verdict struct {
//	            Unsafe bool   `json:"unsafe"`
//	            Reason string `json:"reason"`
//	        }
//	        if err := json.Unmarshal([]byte(resp.Message.Content), &verdict); err != nil {
//	            return agent.GuardrailResult{}, err
//	        }
//	        return agent.GuardrailResult{Tripwire: verdict.Unsafe, Info: verdict.Reason}, nil
//	    },
//	}
package guardrail

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
)

// DenyPatterns returns an input guardrail that trips when the input matches
// any pattern — e.g. credential formats or prompt-injection markers:
//
//	secret := guardrail.DenyPatterns("secrets",
//	    regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
//	    regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`))
//
// Info records the pattern and the offset/length of the match, never the
// matched text itself, so tripping on a real credential does not copy it
// into logs.
func DenyPatterns(name string, patterns ...*regexp.Regexp) agent.InputGuardrail {
	return agent.InputGuardrail{
		Name: name,
		Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
			for _, re := range patterns {
				if re == nil {
					continue
				}
				if loc := re.FindStringIndex(input); loc != nil {
					return agent.GuardrailResult{
						Tripwire: true,
						Info: map[string]any{
							"pattern":      re.String(),
							"match_offset": loc[0],
							"match_len":    loc[1] - loc[0],
						},
					}, nil
				}
			}
			return agent.GuardrailResult{}, nil
		},
	}
}

// MaxLength returns an input guardrail that trips when the input exceeds max
// runes. Its name encodes the limit ("max_length_2000").
func MaxLength(max int) agent.InputGuardrail {
	return agent.InputGuardrail{
		Name: fmt.Sprintf("max_length_%d", max),
		Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
			n := len([]rune(input))
			if n > max {
				return agent.GuardrailResult{
					Tripwire: true,
					Info:     map[string]any{"length": n, "max": max},
				}, nil
			}
			return agent.GuardrailResult{}, nil
		},
	}
}
