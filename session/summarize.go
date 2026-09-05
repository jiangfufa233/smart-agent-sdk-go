package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

// SlidingWindow keeps the leading system messages plus the most recent Keep
// non-system messages. It is a pure truncation: no model calls.
type SlidingWindow struct {
	// Keep is the number of recent messages retained (system messages are
	// always kept in addition). Must be positive.
	Keep int
}

// NewSlidingWindow returns a compressor that retains the most recent keep
// messages.
func NewSlidingWindow(keep int) *SlidingWindow {
	return &SlidingWindow{Keep: keep}
}

func (w *SlidingWindow) Compress(ctx context.Context, history []model.Message) ([]model.Message, error) {
	if w.Keep <= 0 {
		return nil, errors.New("session: sliding window Keep must be positive")
	}
	systems, rest := splitSystem(history)
	if len(rest) <= w.Keep {
		return history, nil
	}
	out := make([]model.Message, 0, len(systems)+w.Keep)
	out = append(out, systems...)
	out = append(out, rest[len(rest)-w.Keep:]...)
	return out, nil
}

const defaultSummarizePrompt = "Summarize the conversation so far, preserving key facts, decisions, " +
	"tool results and open questions. Reply with the summary only."

// Summarizer folds old history into a rolling summary once the conversation
// grows: when the number of non-system messages exceeds High, all but the
// most recent Low are collapsed into a single summary message. Storage
// stays lossless — the summary only shapes the view sent to the model.
//
// The summary rolls: a Summarizer instance caches how much history it has
// already folded, so subsequent compressions fold only the increment into
// the existing summary. A fold happens when High-Low new messages have
// accumulated since the last one (hysteresis); in between, the view is the
// summary plus the still-unfolded increment verbatim — bounded without a
// model call. Overall calls are roughly (len-High)/(High-Low), not one per
// turn. Reuse the same Runner (and thus the same Summarizer) across runs
// to benefit from the cache; a fresh Summarizer re-summarizes from
// scratch. Cache validity is checked via a fingerprint of the last folded
// message, so a cleared or different session triggers a full re-summarize
// instead of a corrupt summary.
type Summarizer struct {
	// Model is called to produce summaries.
	Model model.Model
	// High is the compression threshold on non-system messages (default 50).
	High int
	// Low is the number of recent messages kept verbatim after compression
	// (default 20).
	Low int
	// Prompt overrides the summarization instruction.
	Prompt string

	mu      sync.Mutex
	covered int    // non-system messages already folded into summary
	summary string // rolling summary text; "" before the first fold
	lastKey string // fingerprint of the last folded message
}

// NewSummarizer returns a Summarizer with default thresholds.
func NewSummarizer(m model.Model) *Summarizer {
	return &Summarizer{Model: m}
}

func (s *Summarizer) Compress(ctx context.Context, history []model.Message) ([]model.Message, error) {
	if s.Model == nil {
		return nil, errors.New("session: summarizer has no model configured")
	}
	high, low := s.High, s.Low
	if high <= 0 {
		high = 50
	}
	if low <= 0 {
		low = 20
	}
	if low >= high {
		return nil, fmt.Errorf("session: summarizer Low (%d) must be smaller than High (%d)", low, high)
	}
	increment := high - low

	// One compression at a time: the rolling cache must not interleave.
	s.mu.Lock()
	defer s.mu.Unlock()

	systems, rest := splitSystem(history)
	var older, recent []model.Message
	if len(rest) > low {
		older, recent = rest[:len(rest)-low], rest[len(rest)-low:]
	} else {
		recent = rest
	}

	cacheValid := s.covered > 0 && s.covered <= len(older) &&
		s.lastKey == fingerprint(older[s.covered-1])

	if !cacheValid {
		// No cache yet (first fold), or it went stale (history shrank or a
		// different conversation): fold from scratch, only past High. The
		// stale summary must not leak into the prompt.
		if len(rest) <= high {
			return history, nil
		}
		s.summary, s.covered, s.lastKey = "", 0, ""
		return s.fold(ctx, systems, older, recent, renderMessages(older))
	}

	delta := len(older) - s.covered
	if delta >= increment {
		// Enough new material accumulated: fold the increment into the
		// existing summary (one model call).
		return s.fold(ctx, systems, older, recent, renderMessages(older[s.covered:]))
	}
	// Not worth a model call yet: summary plus the still-unfolded increment
	// verbatim keeps the view bounded (≤ ~High messages) without calling
	// the model on every turn.
	out := make([]model.Message, 0, len(systems)+1+delta+len(recent))
	out = append(out, systems...)
	out = append(out, s.summaryMessage())
	out = append(out, older[s.covered:]...)
	out = append(out, recent...)
	return out, nil
}

// fold sends one summarization request (promptBody holds the messages to
// summarize, either the full older range or the increment) and commits the
// rolling cache on success.
func (s *Summarizer) fold(ctx context.Context, systems, older, recent []model.Message, promptBody string) ([]model.Message, error) {
	var prompt string
	if s.summary != "" {
		prompt = fmt.Sprintf("%s\n\nPrevious summary:\n%s\n\nAdditional messages to fold in:\n%s",
			s.instruction(), s.summary, promptBody)
	} else {
		prompt = fmt.Sprintf("%s\n\nConversation:\n%s", s.instruction(), promptBody)
	}
	resp, err := s.Model.Chat(ctx, &model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("session: summarize: %w", err)
	}
	s.summary = strings.TrimSpace(resp.Message.Content)
	s.covered = len(older)
	s.lastKey = fingerprint(older[len(older)-1])
	return summaryView(systems, s.summary, recent), nil
}

func (s *Summarizer) instruction() string {
	if s.Prompt != "" {
		return s.Prompt
	}
	return defaultSummarizePrompt
}

// summaryMessage wraps the rolling summary text as a user message.
func (s *Summarizer) summaryMessage() model.Message {
	return model.Message{Role: model.RoleUser, Content: "Conversation summary:\n" + s.summary}
}

// summaryView assembles the compressed view: leading system messages, one
// user message carrying the summary, then the recent messages verbatim.
func summaryView(systems []model.Message, summary string, recent []model.Message) []model.Message {
	out := make([]model.Message, 0, len(systems)+1+len(recent))
	out = append(out, systems...)
	out = append(out, model.Message{Role: model.RoleUser, Content: "Conversation summary:\n" + summary})
	out = append(out, recent...)
	return out
}

// fingerprint identifies a message for cache validation. Messages loaded
// from a session round-trip through JSON, so only stable fields are hashed.
func fingerprint(m model.Message) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", m.Role, m.Content, m.Name, m.ToolCallID)
	for _, tc := range m.ToolCalls {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func renderMessages(msgs []model.Message) string {
	var b strings.Builder
	for i := range msgs {
		renderMessage(&b, &msgs[i])
	}
	return b.String()
}

// renderMessage renders one message as a compact "role: content" line for
// the summarizer; tool calls and results are rendered with name and
// arguments instead of raw wire format.
func renderMessage(b *strings.Builder, m *model.Message) {
	switch {
	case m.Role == model.RoleTool:
		fmt.Fprintf(b, "[tool %s] %s\n", m.Name, m.Content)
	case len(m.ToolCalls) > 0:
		b.WriteString("assistant calls tools:")
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(b, " %s(%s);", tc.Function.Name, tc.Function.Arguments)
		}
		b.WriteByte('\n')
		if m.Content != "" {
			fmt.Fprintf(b, "assistant: %s\n", m.Content)
		}
	default:
		fmt.Fprintf(b, "%s: %s\n", m.Role, m.Content)
	}
}
