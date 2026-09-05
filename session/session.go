// Package session provides Session implementations for agent.RunWithSession
// (conversation persistence across runs) and HistoryCompressor
// implementations for Runner.Compressor (view-level history compression).
//
// Storage keeps the full transcript; compression only affects the view
// sent to the model. The system prompt is never stored — it is Agent
// configuration and is prepended fresh on every run.
package session

import (
	"encoding/json"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

var (
	_ agent.Session           = (*InMemory)(nil)
	_ agent.Session           = (*FileSession)(nil)
	_ agent.Session           = (*sqliteSession)(nil)
	_ agent.HistoryCompressor = (*SlidingWindow)(nil)
	_ agent.HistoryCompressor = (*Summarizer)(nil)
)

// splitSystem splits history into its leading system messages and the rest.
func splitSystem(history []model.Message) (systems, rest []model.Message) {
	n := 0
	for n < len(history) && history[n].Role == model.RoleSystem {
		n++
	}
	return history[:n], history[n:]
}

// lastN returns the most recent limit messages in chronological order;
// limit <= 0 returns everything.
func lastN(msgs []model.Message, limit int) []model.Message {
	if limit <= 0 || limit >= len(msgs) {
		return msgs
	}
	return msgs[len(msgs)-limit:]
}

// cloneMessages deep-copies messages so stored history is insulated from
// later caller mutations (Content strings are immutable; slices are not).
func cloneMessages(msgs []model.Message) []model.Message {
	out := make([]model.Message, len(msgs))
	for i, m := range msgs {
		c := m
		if len(m.Parts) > 0 {
			parts := make([]model.ContentPart, len(m.Parts))
			for j, p := range m.Parts {
				if len(p.Extra) > 0 {
					p.Extra = append(json.RawMessage(nil), p.Extra...)
				}
				if p.ImageURL != nil {
					iu := *p.ImageURL
					p.ImageURL = &iu
				}
				parts[j] = p
			}
			c.Parts = parts
		}
		if len(m.ToolCalls) > 0 {
			c.ToolCalls = append([]model.ToolCall(nil), m.ToolCalls...)
		}
		out[i] = c
	}
	return out
}
