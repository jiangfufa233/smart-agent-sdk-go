// Package session provides Session implementations for agent.RunWithSession
// (conversation persistence across runs) and HistoryCompressor
// implementations for Runner.Compressor (view-level history compression).
//
// Storage keeps the full transcript; compression only affects the view
// sent to the model. The system prompt is never stored — it is Agent
// configuration and is prepended fresh on every run.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
)

var (
	_ agent.Session           = (*InMemory)(nil)
	_ agent.Session           = (*FileSession)(nil)
	_ agent.Session           = (*sqliteSession)(nil)
	_ agent.HistoryCompressor = (*SlidingWindow)(nil)
	_ agent.HistoryCompressor = (*Summarizer)(nil)
)

// FormatUpgradeError reports persisted data written by an unsupported session
// storage format. Existing v1 File and SQLite stores must be explicitly
// migrated instead of being mixed with the item-ID format.
type FormatUpgradeError struct {
	Store  string
	Detail string
}

func (e *FormatUpgradeError) Error() string {
	return fmt.Sprintf("session: %s format upgrade required: %s", e.Store, e.Detail)
}

// splitSystem splits history into its leading system messages and the rest.
func splitSystem(history []model.Message) (systems, rest []model.Message) {
	n := 0
	for n < len(history) && history[n].Role == model.RoleSystem {
		n++
	}
	return history[:n], history[n:]
}

// lastNItems returns the most recent limit items in chronological order;
// limit <= 0 returns everything.
func lastNItems(items []agent.SessionItem, limit int) []agent.SessionItem {
	if limit <= 0 || limit >= len(items) {
		return items
	}
	return items[len(items)-limit:]
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

func cloneSessionItems(items []agent.SessionItem) []agent.SessionItem {
	out := make([]agent.SessionItem, len(items))
	messages := make([]model.Message, len(items))
	for i, item := range items {
		out[i].ID = item.ID
		messages[i] = item.Message
	}
	messages = cloneMessages(messages)
	for i := range out {
		out[i].Message = messages[i]
	}
	return out
}

func newSessionItems(messages []model.Message) ([]agent.SessionItem, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	out := make([]agent.SessionItem, len(messages))
	cloned := cloneMessages(messages)
	for i := range cloned {
		id, err := newSessionItemID()
		if err != nil {
			return nil, fmt.Errorf("session: generate item ID: %w", err)
		}
		out[i] = agent.SessionItem{ID: id, Message: cloned[i]}
	}
	return out, nil
}

func newSessionItemID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func replacementBounds(items []agent.SessionItem, firstID, lastID string) (int, int, error) {
	first, last := -1, -1
	for i, item := range items {
		if item.ID == firstID {
			first = i
		}
		if item.ID == lastID {
			last = i
		}
	}
	if first < 0 || last < 0 {
		return 0, 0, &agent.SessionRangeError{
			FirstID: firstID,
			LastID:  lastID,
			Reason:  "unknown item ID",
		}
	}
	if first > last {
		return 0, 0, &agent.SessionRangeError{
			FirstID: firstID,
			LastID:  lastID,
			Reason:  "range is reversed",
		}
	}
	return first, last, nil
}
