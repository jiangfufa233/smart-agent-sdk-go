// Package soak exercises the SDK's runtime surfaces under sustained load:
// streaming tool loops, handoffs, retry/fallback fault injection, all three
// session stores and both history compressors. Tests skip unless the
// SOAK_ITERS environment variable is set, keeping CI fast:
//
//	SOAK_ITERS=3000 go test ./soak
//
// Each scenario snapshots goroutines, heap and open file descriptors after a
// warmup window and re-checks them at the end, so gradual resource loss
// fails the run instead of silently soaking. goleak guards the whole
// package.
package soak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/audit"
	"github.com/jiangfufa233/smart-agent-sdk-go/guardrail"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/session"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const envIters = "SOAK_ITERS"

func soakIters(t *testing.T) int {
	t.Helper()
	n, err := strconv.Atoi(os.Getenv(envIters))
	if err != nil || n <= 0 {
		t.Skipf("soak disabled: set %s=<n> to run (e.g. SOAK_ITERS=3000 go test ./soak)", envIters)
	}
	return n
}

// snap captures the resource counters a scenario is checked against.
type snap struct {
	heapAlloc   uint64
	heapObjects uint64
	goroutines  int
	fds         int // -1 when /proc is unavailable (non-Linux)
}

func takeSnap() snap {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := snap{
		heapAlloc:   ms.HeapAlloc,
		heapObjects: ms.HeapObjects,
		goroutines:  runtime.NumGoroutine(),
		fds:         -1,
	}
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		s.fds = len(entries)
	}
	return s
}

func checkNoLeaks(t *testing.T, scenario string, before, after snap, maxHeapBytes, maxHeapObjects uint64, maxExtraFDs, maxExtraGoroutines int) {
	t.Helper()
	if after.heapAlloc > before.heapAlloc+maxHeapBytes {
		t.Errorf("%s: HeapAlloc grew beyond limit (before %d, after %d, limit +%d)",
			scenario, before.heapAlloc, after.heapAlloc, maxHeapBytes)
	}
	if after.heapObjects > before.heapObjects+maxHeapObjects {
		t.Errorf("%s: HeapObjects grew beyond limit (before %d, after %d, limit +%d)",
			scenario, before.heapObjects, after.heapObjects, maxHeapObjects)
	}
	if after.goroutines > before.goroutines+maxExtraGoroutines {
		t.Errorf("%s: goroutines leaked (before %d, after %d, limit +%d)",
			scenario, before.goroutines, after.goroutines, maxExtraGoroutines)
	}
	if before.fds >= 0 && after.fds > before.fds+maxExtraFDs {
		t.Errorf("%s: file descriptors leaked (before %d, after %d, limit +%d)",
			scenario, before.fds, after.fds, maxExtraFDs)
	}
}

func timed(t *testing.T, name string, n int, body func(i int)) {
	t.Helper()
	start := time.Now()
	for i := 0; i < n; i++ {
		body(i)
	}
	elapsed := time.Since(start)
	t.Logf("%s: %d iterations in %s (%.0f iters/s)",
		name, n, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds())
}

// ---- offline models (no request recording: measured memory is the SDK's) ----

type echoArgs struct {
	Text string `json:"text"`
}

var echoTool, _ = tool.NewFunction("echo", "echoes its input",
	func(ctx context.Context, in echoArgs) (string, error) { return in.Text, nil })

var passOutput = agent.OutputGuardrail{
	Name: "soak-pass",
	Guardrail: func(ctx context.Context, a *agent.Agent, res *agent.RunResult) (agent.GuardrailResult, error) {
		return agent.GuardrailResult{}, nil
	},
}

var auditLogger = audit.NewSlog(slog.New(slog.NewJSONHandler(io.Discard, nil)))

// loopModel scripts a tool call on the first model call and a final text
// answer afterwards; a transfer tool in the request wins (handoff runs).
// Agents without tools get text immediately. One instance per run.
type loopModel struct {
	calls atomic.Int32
}

func hasToolNamed(req *model.Request, name string) bool {
	for _, tp := range req.Tools {
		if tp.Function.Name == name {
			return true
		}
	}
	return false
}

func (m *loopModel) respond(req *model.Request) model.Response {
	n := m.calls.Add(1)
	switch {
	case hasToolNamed(req, "transfer_to_b"):
		return model.Response{
			Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
				ID: "t1", Type: "function", Function: model.FunctionCall{Name: "transfer_to_b", Arguments: "{}"},
			}}},
			FinishReason: "tool_calls",
			Usage:        model.Usage{TotalTokens: 1},
		}
	case hasToolNamed(req, "echo") && n == 1:
		return model.Response{
			Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
				ID: "c1", Type: "function", Function: model.FunctionCall{Name: "echo", Arguments: `{"text":"hi"}`},
			}}},
			FinishReason: "tool_calls",
			Usage:        model.Usage{TotalTokens: 1},
		}
	default:
		return model.Response{
			Message:      model.Message{Role: model.RoleAssistant, Content: "answer 2"},
			FinishReason: "stop",
			Usage:        model.Usage{TotalTokens: 1},
		}
	}
}

func (m *loopModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	r := m.respond(req)
	return &r, nil
}

func (m *loopModel) ChatStream(ctx context.Context, req *model.Request) (model.StreamReader, error) {
	r := m.respond(req)
	var events []model.StreamEvent
	for _, tc := range r.Message.ToolCalls {
		events = append(events, model.StreamEvent{Type: model.StreamToolCallDelta, ToolCall: tc})
	}
	if r.Message.Content != "" {
		events = append(events,
			model.StreamEvent{Type: model.StreamTextDelta, Text: "answer "},
			model.StreamEvent{Type: model.StreamTextDelta, Text: "2"},
		)
	}
	events = append(events, model.StreamEvent{Type: model.StreamFinish, FinishReason: r.FinishReason, Usage: r.Usage})
	return &sliceReader{events: events}, nil
}

type sliceReader struct {
	events []model.StreamEvent
	i      int
	cur    model.StreamEvent
}

func (r *sliceReader) Next() bool {
	if r.i >= len(r.events) {
		return false
	}
	r.cur = r.events[r.i]
	r.i++
	return true
}

func (r *sliceReader) Event() model.StreamEvent { return r.cur }
func (r *sliceReader) Err() error               { return nil }
func (r *sliceReader) Close() error             { return nil }

// flakyModel fails its first fails calls with a retryable 429, then answers.
type flakyModel struct {
	fails int
	calls atomic.Int32
}

func (m *flakyModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	if int(m.calls.Add(1)) <= m.fails {
		return nil, model.NewHTTPError("soak", 429, "rate limited")
	}
	return &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: "recovered"},
		FinishReason: "stop",
	}, nil
}

// textModel answers with a fixed string; stateless and shareable.
type textModel struct{ content string }

func (m *textModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	return &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: m.content},
		FinishReason: "stop",
		Usage:        model.Usage{TotalTokens: 1},
	}, nil
}

// countingModel counts summarizer invocations without recording requests.
type countingModel struct {
	calls atomic.Int32
}

func (m *countingModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	m.calls.Add(1)
	return &model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: "summary"},
		FinishReason: "stop",
	}, nil
}

func (m *countingModel) count() int { return int(m.calls.Load()) }

// measuringModel records the largest request seen, in messages.
type measuringModel struct {
	inner model.Model
	max   atomic.Int32
}

func (m *measuringModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	for {
		cur := m.max.Load()
		if len(req.Messages) <= int(cur) || m.max.CompareAndSwap(cur, int32(len(req.Messages))) {
			break
		}
	}
	return m.inner.Chat(ctx, req)
}

// ---- scenarios ----

func TestSoakStreamLoop(t *testing.T) {
	n := soakIters(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runner := &agent.Runner{Hooks: auditLogger}

	run := func(i int) (*agent.RunResult, *agent.Agent, *agent.Agent, error) {
		b := &agent.Agent{Name: "b", Instructions: "soak target", Model: &loopModel{}}
		a := &agent.Agent{
			Name:             "a",
			Instructions:     "soak",
			Model:            &loopModel{},
			Tools:            []tool.Tool{echoTool},
			InputGuardrails:  []agent.InputGuardrail{guardrail.MaxLength(1 << 20)},
			OutputGuardrails: []agent.OutputGuardrail{passOutput},
		}
		if i%4 == 0 {
			a.Handoffs = []agent.Handoff{{Target: b}}
		}
		res, err := runner.RunStream(ctx, a, "ping").Wait()
		return res, a, b, err
	}

	for i := 0; i < 50; i++ {
		if _, _, _, err := run(i); err != nil {
			t.Fatalf("warmup run %d: %v", i, err)
		}
	}
	before := takeSnap()
	timed(t, "stream loop", n, func(i int) {
		res, a, b, err := run(i)
		if err != nil {
			t.Errorf("run %d: %v", i, err)
			return
		}
		if res.Output != "answer 2" {
			t.Errorf("run %d: output %q, want %q", i, res.Output, "answer 2")
		}
		if len(res.ToolErrors) != 0 {
			t.Errorf("run %d: unexpected tool errors: %v", i, res.ToolErrors)
		}
		if i%4 == 0 {
			if len(res.Transfers) != 1 || res.Transfers[0] != "b" || res.Agent != b {
				t.Errorf("run %d: handoff not taken: transfers=%v agent=%v", i, res.Transfers, res.Agent)
			}
		} else if res.Agent != a {
			t.Errorf("run %d: agent changed without handoff", i)
		}
	})
	checkNoLeaks(t, "stream loop", before, takeSnap(), 8<<20, 100_000, 2, 0)
}

func TestSoakFaultInjection(t *testing.T) {
	n := soakIters(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runner := &agent.Runner{Hooks: auditLogger}
	policy := model.RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, Multiplier: 2}

	run := func(i int) (*agent.RunResult, error) {
		switch i % 3 {
		case 0: // transient 429s absorbed by WithRetry
			a := &agent.Agent{Name: "retry", Model: model.WithRetry(&flakyModel{fails: 2}, policy)}
			return runner.Run(ctx, a, "ping")
		case 1: // permanent primary failure falls through Fallback
			a := &agent.Agent{Name: "fallback", Model: model.Fallback(
				model.WithRetry(&flakyModel{fails: 99}, policy),
				&textModel{content: "backup"},
			)}
			return runner.Run(ctx, a, "ping")
		default: // the tool fails on its first call; the run still completes
			var calls atomic.Int32
			flakyTool, err := tool.NewFunction("echo", "echoes its input",
				func(ctx context.Context, in echoArgs) (string, error) {
					if calls.Add(1) == 1 {
						return "", errors.New("boom")
					}
					return in.Text, nil
				})
			if err != nil {
				return nil, err
			}
			a := &agent.Agent{Name: "toolfail", Model: &loopModel{}, Tools: []tool.Tool{flakyTool}}
			return runner.Run(ctx, a, "ping")
		}
	}

	for i := 0; i < 30; i++ {
		if _, err := run(i); err != nil {
			t.Fatalf("warmup run %d: %v", i, err)
		}
	}
	before := takeSnap()
	timed(t, "fault injection", n, func(i int) {
		res, err := run(i)
		if err != nil {
			t.Errorf("run %d: %v", i, err)
			return
		}
		switch i % 3 {
		case 0:
			if res.Output != "recovered" {
				t.Errorf("run %d: output %q, want %q", i, res.Output, "recovered")
			}
		case 1:
			if res.Output != "backup" {
				t.Errorf("run %d: output %q, want %q", i, res.Output, "backup")
			}
		default:
			if res.Output != "answer 2" {
				t.Errorf("run %d: output %q, want %q", i, res.Output, "answer 2")
			}
			if len(res.ToolErrors) != 1 || res.ToolErrors[0].Tool != "echo" {
				t.Errorf("run %d: tool errors = %v, want one echo failure", i, res.ToolErrors)
			}
		}
	})
	checkNoLeaks(t, "fault injection", before, takeSnap(), 8<<20, 100_000, 2, 0)
}

func TestSoakSessions(t *testing.T) {
	n := soakIters(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	runner := &agent.Runner{Hooks: auditLogger, Compressor: session.NewSlidingWindow(32)}
	dir := t.TempDir()
	mem := session.NewInMemory()
	file := session.NewFile(filepath.Join(dir, "soak.jsonl"))
	store, err := session.NewSQLiteStore(filepath.Join(dir, "soak.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer func() { _ = store.Close() }()
	hot := store.Get("hot")

	chat := &agent.Agent{Name: "chat", Model: &textModel{content: "ok"}}

	const workers = 8
	runsPerWorker := n / workers
	if runsPerWorker == 0 {
		runsPerWorker = 1
	}
	churnPerWorker := runsPerWorker / 25

	if _, err := runner.RunWithSession(ctx, chat, mem, "warm"); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if err := mem.Clear(ctx); err != nil {
		t.Fatalf("clear after warmup: %v", err)
	}

	before := takeSnap()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			sm := store.Get(fmt.Sprintf("w%d", w))
			for i := 0; i < runsPerWorker; i++ {
				if _, err := runner.RunWithSession(ctx, chat, mem, fmt.Sprintf("m%d-%d", w, i)); err != nil {
					t.Errorf("worker %d: in-memory run %d: %v", w, i, err)
					return
				}
				var target agent.Session
				target = sm
				if i%2 == 1 {
					target = file
				}
				if _, err := runner.RunWithSession(ctx, chat, target, fmt.Sprintf("s%d-%d", w, i)); err != nil {
					t.Errorf("worker %d: session run %d: %v", w, i, err)
					return
				}
				if _, err := sm.GetItems(ctx, 10); err != nil {
					t.Errorf("worker %d: sqlite get: %v", w, err)
					return
				}
				if _, err := file.GetItems(ctx, 10); err != nil {
					t.Errorf("worker %d: file get: %v", w, err)
					return
				}
			}
			for j := 0; j < churnPerWorker; j++ {
				item := model.Message{Role: model.RoleUser, Content: fmt.Sprintf("churn %d-%d", w, j)}
				if err := hot.AddItems(ctx, []model.Message{item}); err != nil {
					t.Errorf("worker %d: churn add: %v", w, err)
					return
				}
				if _, err := hot.GetItems(ctx, 5); err != nil {
					t.Errorf("worker %d: churn get: %v", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Exact write counts: concurrent runs must not lose or duplicate items.
	items, err := mem.GetItems(ctx, -1)
	if err != nil {
		t.Fatalf("read in-memory session: %v", err)
	}
	if want := 2 * runsPerWorker * workers; len(items) != want {
		t.Errorf("in-memory session: got %d items, want %d", len(items), want)
	}
	items, err = store.Get("w0").GetItems(ctx, -1)
	if err != nil {
		t.Fatalf("read sqlite session: %v", err)
	}
	if want := 2 * ((runsPerWorker + 1) / 2); len(items) != want {
		t.Errorf("sqlite session w0: got %d items, want %d", len(items), want)
	}
	items, err = file.GetItems(ctx, -1)
	if err != nil {
		t.Fatalf("read file session: %v", err)
	}
	if want := 2 * (runsPerWorker - (runsPerWorker+1)/2) * workers; len(items) != want {
		t.Errorf("file session: got %d items, want %d", len(items), want)
	}
	if churnPerWorker > 0 {
		items, err = hot.GetItems(ctx, -1)
		if err != nil {
			t.Fatalf("read hot sqlite session: %v", err)
		}
		if want := churnPerWorker * workers; len(items) != want {
			t.Errorf("hot sqlite session: got %d items, want %d", len(items), want)
		}
	}
	checkNoLeaks(t, "sessions", before, takeSnap(), 32<<20, 400_000, 2, 0)
}

func TestSoakCompressors(t *testing.T) {
	n := soakIters(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Phase 1: Summarizer over a growing session. Lossless storage plus
	// hysteresis means the summarizer model is called ~once per High-Low
	// threshold crossings, not once per run.
	sum := &countingModel{}
	comp := session.NewSummarizer(sum)
	comp.High, comp.Low = 20, 8
	runner := &agent.Runner{Hooks: auditLogger, Compressor: comp}
	sess := session.NewInMemory()
	chat := &agent.Agent{Name: "chat", Model: &textModel{content: "ok"}}

	if _, err := runner.RunWithSession(ctx, chat, sess, "warm"); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if err := sess.Clear(ctx); err != nil {
		t.Fatalf("clear after warmup: %v", err)
	}
	before := takeSnap()
	timed(t, "summarizer", n, func(i int) {
		if _, err := runner.RunWithSession(ctx, chat, sess, fmt.Sprintf("q%d", i)); err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	})
	items, err := sess.GetItems(ctx, -1)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if len(items) != 2*n {
		t.Errorf("lossless storage violated: got %d items, want %d", len(items), 2*n)
	}
	calls := sum.count()
	// Each run adds 2 non-system messages; a fold happens at most every
	// High-Low of them, plus slack for the initial compression.
	if maxCalls := 2*n/(20-8) + 8; calls > maxCalls {
		t.Errorf("summarizer called %d times, want <= %d (hysteresis broken?)", calls, maxCalls)
	}
	t.Logf("summarizer: %d model calls for %d runs (storage: %d items)", calls, n, len(items))
	checkNoLeaks(t, "summarizer", before, takeSnap(), 32<<20, 400_000, 2, 0)

	// Phase 2: SlidingWindow must bound the model view no matter how long
	// the session grows: leading system + Keep + this run's input.
	window := session.NewSlidingWindow(16)
	measured := &measuringModel{inner: &textModel{content: "ok"}}
	runner2 := &agent.Runner{Hooks: auditLogger, Compressor: window}
	sess2 := session.NewInMemory()
	chat2 := &agent.Agent{Name: "chat2", Model: measured}
	for i := 0; i < 500; i++ {
		if _, err := runner2.RunWithSession(ctx, chat2, sess2, fmt.Sprintf("w%d", i)); err != nil {
			t.Fatalf("window run %d: %v", i, err)
		}
	}
	if got := measured.max.Load(); got > 18 {
		t.Errorf("model saw %d messages, sliding window should bound the view at 18", got)
	}
}
