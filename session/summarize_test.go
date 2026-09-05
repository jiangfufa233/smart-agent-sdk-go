package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

func TestSlidingWindow(t *testing.T) {
	ctx := context.Background()
	sys := model.Message{Role: model.RoleSystem, Content: "be terse"}

	t.Run("keeps leading system plus the most recent messages", func(t *testing.T) {
		w := NewSlidingWindow(3)
		history := append([]model.Message{sys}, rangeMsgs(5)...)
		out, err := w.Compress(ctx, history)
		if err != nil {
			t.Fatalf("Compress: %v", err)
		}
		if out[0].Role != model.RoleSystem {
			t.Fatalf("system message dropped: %+v", out[0])
		}
		assertContents(t, out, "be terse", "msg-3", "msg-4", "msg-5")
	})

	t.Run("multiple system messages are preserved", func(t *testing.T) {
		w := NewSlidingWindow(2)
		history := append([]model.Message{sys, {Role: model.RoleSystem, Content: "also sys"}}, rangeMsgs(4)...)
		out, err := w.Compress(ctx, history)
		if err != nil {
			t.Fatalf("Compress: %v", err)
		}
		assertContents(t, out, "be terse", "also sys", "msg-3", "msg-4")
	})

	t.Run("short history is returned unchanged", func(t *testing.T) {
		w := NewSlidingWindow(10)
		history := rangeMsgs(3)
		out, err := w.Compress(ctx, history)
		if err != nil {
			t.Fatalf("Compress: %v", err)
		}
		assertContents(t, out, "msg-1", "msg-2", "msg-3")
	})

	t.Run("non-positive keep is rejected", func(t *testing.T) {
		if _, err := NewSlidingWindow(0).Compress(ctx, rangeMsgs(3)); err == nil {
			t.Fatal("expected error for Keep=0")
		}
	})
}

func TestSummarizerBelowThreshold(t *testing.T) {
	// An empty scripted model fails the test if a summarization call is made.
	s := NewSummarizer(testutil.NewScripted())
	history := append([]model.Message{{Role: model.RoleSystem, Content: "sys"}}, rangeMsgs(10)...)
	out, err := s.Compress(context.Background(), history)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	assertContents(t, out, append([]string{"sys"}, contents(rangeMsgs(10))...)...)
}

func TestSummarizerTriggersAndShape(t *testing.T) {
	ctx := context.Background()
	var prompt string
	s := NewSummarizer(testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		prompt = req.Messages[0].Content
		return testutil.TextStep("sum-v1").Resp, nil
	}}))
	s.High, s.Low = 4, 2

	history := append([]model.Message{{Role: model.RoleSystem, Content: "sys"}}, rangeMsgs(6)...)
	out, err := s.Compress(ctx, history)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	if len(out) != 4 {
		t.Fatalf("view has %d messages, want 4: %v", len(out), contents(out))
	}
	if out[0].Role != model.RoleSystem || out[0].Content != "sys" {
		t.Fatalf("system message not preserved: %+v", out[0])
	}
	if out[1].Role != model.RoleUser || !strings.HasPrefix(out[1].Content, "Conversation summary:\n") {
		t.Fatalf("summary message malformed: %+v", out[1])
	}
	if !strings.HasSuffix(out[1].Content, "sum-v1") {
		t.Fatalf("summary text missing: %q", out[1].Content)
	}
	assertContents(t, out[2:], "msg-5", "msg-6")

	// The prompt rendered the folded messages, none of the recent ones.
	for _, want := range []string{"user: msg-1", "user: msg-2", "user: msg-3", "user: msg-4"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "msg-5") || strings.Contains(prompt, "Previous summary") {
		t.Fatalf("prompt should contain only the older range on first fold:\n%s", prompt)
	}
}

// countingModel answers every summarization call with "sum-<n>" and
// records each prompt.
type countingModel struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (c *countingModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.prompts = append(c.prompts, req.Messages[0].Content)
	return &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: fmt.Sprintf("sum-%d", c.calls)},
		FinishReason: "stop",
	}, nil
}

func TestSummarizerRollingCache(t *testing.T) {
	ctx := context.Background()
	cm := &countingModel{}
	s := NewSummarizer(cm)
	s.High, s.Low = 4, 2
	sys := model.Message{Role: model.RoleSystem, Content: "sys"}

	hist := func(n int) []model.Message {
		return append([]model.Message{sys}, rangeMsgs(n)...)
	}

	// 1st: full fold of m1..m4.
	out, err := s.Compress(ctx, hist(6))
	if err != nil {
		t.Fatalf("compress 6: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nsum-1", "msg-5", "msg-6")

	// +1 growth: below the increment (High-Low), no model call, but the
	// view stays bounded (summary + unfolded increment + recent).
	out, err = s.Compress(ctx, hist(7))
	if err != nil {
		t.Fatalf("compress 7: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nsum-1", "msg-5", "msg-6", "msg-7")

	// 2nd: increment reached (m5, m6 folded into the existing summary).
	out, err = s.Compress(ctx, hist(8))
	if err != nil {
		t.Fatalf("compress 8: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nsum-2", "msg-7", "msg-8")
	if !strings.Contains(cm.prompts[1], "Previous summary:\nsum-1") {
		t.Fatalf("incremental prompt must carry the previous summary:\n%s", cm.prompts[1])
	}
	for _, want := range []string{"user: msg-5", "user: msg-6"} {
		if !strings.Contains(cm.prompts[1], want) {
			t.Fatalf("incremental prompt missing %q:\n%s", want, cm.prompts[1])
		}
	}
	if strings.Contains(cm.prompts[1], "user: msg-1") {
		t.Fatalf("incremental prompt must not re-send already folded messages:\n%s", cm.prompts[1])
	}

	// No growth: reuse, no call.
	if _, err := s.Compress(ctx, hist(8)); err != nil {
		t.Fatalf("compress 8 again: %v", err)
	}
	// +1 growth: still below the increment, no call.
	out, err = s.Compress(ctx, hist(9))
	if err != nil {
		t.Fatalf("compress 9: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nsum-2", "msg-7", "msg-8", "msg-9")

	// 3rd fold: m7, m8 folded.
	if _, err := s.Compress(ctx, hist(10)); err != nil {
		t.Fatalf("compress 10: %v", err)
	}
	if cm.calls != 3 {
		t.Fatalf("summarizer called %d times, want 3", cm.calls)
	}
}

func TestSummarizerCacheInvalidatedByNewConversation(t *testing.T) {
	ctx := context.Background()
	cm := &countingModel{}
	s := NewSummarizer(cm)
	s.High, s.Low = 4, 2
	sys := model.Message{Role: model.RoleSystem, Content: "sys"}

	first := append([]model.Message{sys}, rangeMsgs(6)...)
	if _, err := s.Compress(ctx, first); err != nil {
		t.Fatalf("compress 1: %v", err)
	}

	// A different conversation: the boundary fingerprint no longer matches,
	// so the summarizer must re-fold from scratch instead of corrupting the
	// summary.
	second := append([]model.Message{sys}, func() []model.Message {
		out := make([]model.Message, 6)
		for i := range out {
			out[i] = userMsg(fmt.Sprintf("n-%d", i+1))
		}
		return out
	}()...)
	out, err := s.Compress(ctx, second)
	if err != nil {
		t.Fatalf("compress 2: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nsum-2", "n-5", "n-6")
	if strings.Contains(cm.prompts[1], "Previous summary") {
		t.Fatalf("stale cache must trigger a full re-summarize:\n%s", cm.prompts[1])
	}
	if !strings.Contains(cm.prompts[1], "user: n-1") {
		t.Fatalf("full re-summarize should render all older messages:\n%s", cm.prompts[1])
	}
}

func TestSummarizerModelFailureLeavesCacheClean(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewScripted(
		testutil.HTTPErrorStep(500, "summarizer down"),
		testutil.TextStep("recovered summary"),
	)
	s := NewSummarizer(m)
	s.High, s.Low = 4, 2
	sys := model.Message{Role: model.RoleSystem, Content: "sys"}

	history := append([]model.Message{sys}, rangeMsgs(6)...)
	if _, err := s.Compress(ctx, history); err == nil {
		t.Fatal("expected summarization failure")
	}

	// The failed call must not count as folded: the retry re-sends the full
	// older range and succeeds.
	out, err := s.Compress(ctx, history)
	if err != nil {
		t.Fatalf("retry compress: %v", err)
	}
	assertContents(t, out, "sys", "Conversation summary:\nrecovered summary", "msg-5", "msg-6")
}

func TestSummarizerValidation(t *testing.T) {
	ctx := context.Background()

	if _, err := NewSummarizer(nil).Compress(ctx, rangeMsgs(60)); err == nil {
		t.Fatal("expected error for nil model")
	}

	s := NewSummarizer(testutil.NewScripted())
	s.High, s.Low = 2, 2
	if _, err := s.Compress(ctx, rangeMsgs(60)); err == nil {
		t.Fatal("expected error for Low >= High")
	}
}
