package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

func tripInput(name string) InputGuardrail {
	return InputGuardrail{Name: name, Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		return GuardrailResult{Tripwire: true, Info: "denied: " + input}, nil
	}}
}

func passInput(name string) InputGuardrail {
	return InputGuardrail{Name: name, Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		return GuardrailResult{}, nil
	}}
}

func tripOutput(name string) OutputGuardrail {
	return OutputGuardrail{Name: name, Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
		return GuardrailResult{Tripwire: true, Info: "leak: " + res.Output}, nil
	}}
}

func TestInputGuardrailTripwire(t *testing.T) {
	// The script is empty: any model call fails the run with a different
	// error, proving tripwires fire before the first model call.
	a := &Agent{Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{tripInput("block")}}
	_, err := NewRunner().Run(context.Background(), a, "hi")
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) {
		t.Fatalf("err = %v, want *GuardrailTripwireError", err)
	}
	if !errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("errors.Is(ErrGuardrailTripwire) = false for %v", err)
	}
	if trip.Stage != StageInput || trip.Guardrail != "block" || trip.Info != "denied: hi" {
		t.Fatalf("trip = %+v", trip)
	}
	if !strings.Contains(err.Error(), `input guardrail "block" tripped`) {
		t.Fatalf("error text = %q", err.Error())
	}
}

func TestInputGuardrailsAllRun(t *testing.T) {
	var mu sync.Mutex
	var called []string
	first := InputGuardrail{Name: "first", Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		mu.Lock()
		called = append(called, "first")
		mu.Unlock()
		return GuardrailResult{Tripwire: true}, nil
	}}
	second := InputGuardrail{Name: "second", Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		mu.Lock()
		called = append(called, "second")
		mu.Unlock()
		return GuardrailResult{}, nil
	}}
	a := &Agent{Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{first, second}}
	_, err := NewRunner().Run(context.Background(), a, "x")
	if !errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("err = %v", err)
	}
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) || trip.Guardrail != "first" {
		t.Fatalf("trip = %+v, want first (declaration order)", trip)
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(called)
	if strings.Join(called, ",") != "first,second" {
		t.Fatalf("called = %v, want all guardrails to run", called)
	}
}

func TestInputGuardrailFailClosed(t *testing.T) {
	sentinel := errors.New("moderation backend down")
	g := InputGuardrail{Name: "mod", Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		return GuardrailResult{}, sentinel
	}}
	a := &Agent{Model: testutil.NewScripted(testutil.TextStep("unused")), InputGuardrails: []InputGuardrail{g}}
	_, err := NewRunner().Run(context.Background(), a, "x")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrap of %v", err, sentinel)
	}
	if errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("a failed guardrail must not look like a tripwire: %v", err)
	}
	if !strings.Contains(err.Error(), `input guardrail "mod" failed`) {
		t.Fatalf("error text = %q", err.Error())
	}
}

func TestOutputGuardrailTripwire(t *testing.T) {
	m := testutil.NewScripted(testutil.TextStep("final answer"))
	g := OutputGuardrail{Name: "leak", Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
		return GuardrailResult{Tripwire: true}, nil
	}}
	_, err := NewRunner().Run(context.Background(), &Agent{Model: m, OutputGuardrails: []OutputGuardrail{g}}, "hi")
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) {
		t.Fatalf("err = %v, want *GuardrailTripwireError", err)
	}
	if trip.Stage != StageOutput || trip.Guardrail != "leak" {
		t.Fatalf("trip = %+v", trip)
	}
	if m.Calls() != 1 {
		t.Fatalf("model calls = %d, want 1", m.Calls())
	}
}

func TestOutputGuardrailSeesResult(t *testing.T) {
	m := testutil.NewScripted(testutil.TextStep("final answer"))
	var seen *RunResult
	g := OutputGuardrail{Name: "check", Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
		seen = res
		return GuardrailResult{}, nil
	}}
	a := &Agent{Model: m, OutputGuardrails: []OutputGuardrail{g}}
	res, err := NewRunner().Run(context.Background(), a, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Output != "final answer" || seen.Agent != a {
		t.Fatalf("guardrail saw res = %+v", seen)
	}
	if res.Output != "final answer" {
		t.Fatalf("res.Output = %q", res.Output)
	}
}

func TestOutputGuardrailOfFinalAgent(t *testing.T) {
	supervisorGuardRan := false
	supervisor := handoffAgent("triage", "a sys", testutil.NewScripted(
		testutil.ToolCallStep("h1", "transfer_to_specialist", "{}"),
	), Handoff{Target: handoffAgent("specialist", "b sys", testutil.NewScripted(testutil.TextStep("spec answer")))})
	supervisor.OutputGuardrails = []OutputGuardrail{{Name: "supervisor-guard", Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
		supervisorGuardRan = true
		return GuardrailResult{}, nil
	}}}
	specialist := supervisor.Handoffs[0].Target
	specialist.OutputGuardrails = []OutputGuardrail{tripOutput("spec-guard")}

	_, err := NewRunner().Run(context.Background(), supervisor, "x")
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) {
		t.Fatalf("err = %v, want *GuardrailTripwireError", err)
	}
	if trip.Guardrail != "spec-guard" {
		t.Fatalf("trip.Guardrail = %q, want the final agent's guardrail", trip.Guardrail)
	}
	if supervisorGuardRan {
		t.Fatal("supervisor output guardrails must not run after a handoff")
	}
}

func TestInputGuardrailsConcurrent(t *testing.T) {
	sleep := InputGuardrail{Name: "sleep", Guardrail: func(ctx context.Context, a *Agent, input string) (GuardrailResult, error) {
		time.Sleep(300 * time.Millisecond)
		return GuardrailResult{}, nil
	}}
	a := &Agent{Model: testutil.NewScripted(testutil.TextStep("ok")), InputGuardrails: []InputGuardrail{sleep, sleep}}
	started := time.Now()
	if _, err := NewRunner().Run(context.Background(), a, "x"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 550*time.Millisecond {
		t.Fatalf("run took %v; guardrails did not run concurrently", elapsed)
	}
}

func TestGuardrailValidation(t *testing.T) {
	a := &Agent{Model: testutil.NewScripted(testutil.TextStep("x")), InputGuardrails: []InputGuardrail{{Name: "no-fn"}}}
	_, err := NewRunner().Run(context.Background(), a, "hi")
	if err == nil || !strings.Contains(err.Error(), `input guardrail "no-fn" has no function configured`) {
		t.Fatalf("err = %v", err)
	}
	b := &Agent{Model: testutil.NewScripted(testutil.TextStep("x")), OutputGuardrails: []OutputGuardrail{{}}}
	_, err = NewRunner().Run(context.Background(), b, "hi")
	if err == nil || !strings.Contains(err.Error(), `output guardrail "guardrail" has no function configured`) {
		t.Fatalf("err = %v", err)
	}
}

// guardrailRecorder records guardrail verdicts inline with the base recorder
// events, which makes cross-hook ordering assertable.
type guardrailRecorder struct {
	*recorder
	verdicts []string
}

func (g *guardrailRecorder) OnGuardrail(ctx context.Context, a *Agent, runID string, stage GuardrailStage, name string, res GuardrailResult, err error, elapsed time.Duration) {
	g.events = append(g.events, "guardrail:"+string(stage)+":"+name)
	v := string(stage) + ":" + name + ":"
	if err != nil {
		v += "error"
	} else if res.Tripwire {
		v += "trip"
	} else {
		v += "pass"
	}
	g.verdicts = append(g.verdicts, v)
}

func TestGuardrailHookEvents(t *testing.T) {
	rec := &guardrailRecorder{recorder: &recorder{}}
	m := testutil.NewScripted(testutil.TextStep("ok"))
	a := &Agent{Model: m,
		InputGuardrails: []InputGuardrail{passInput("in-check")},
		OutputGuardrails: []OutputGuardrail{{Name: "out-check", Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
			return GuardrailResult{}, nil
		}}},
	}
	if _, err := (&Runner{Hooks: rec}).Run(context.Background(), a, "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run_start",
		"guardrail:input:in-check",
		"llm_call:0", "llm_response",
		"guardrail:output:out-check",
		"run_end",
	}
	if strings.Join(rec.events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
	if strings.Join(rec.verdicts, ",") != "input:in-check:pass,output:out-check:pass" {
		t.Fatalf("verdicts = %v", rec.verdicts)
	}

	rec2 := &guardrailRecorder{recorder: &recorder{}}
	trip := &Agent{Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{tripInput("block")}}
	if _, err := (&Runner{Hooks: rec2}).Run(context.Background(), trip, "x"); !errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(rec2.verdicts, ",") != "input:block:trip" {
		t.Fatalf("verdicts = %v", rec2.verdicts)
	}
}

func TestSlogHooksGuardrail(t *testing.T) {
	var buf bytes.Buffer
	hooks := SlogHooks(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	a := &Agent{Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{tripInput("block")}}
	if _, err := (&Runner{Hooks: hooks}).Run(context.Background(), a, "x"); !errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("err = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"guardrail tripwire", `"stage":"input"`, `"guardrail":"block"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}

func TestMultiHooks(t *testing.T) {
	plain := &recorder{}
	hand := &handoffRecorder{recorder: &recorder{}}
	gr := &guardrailRecorder{recorder: &recorder{}}
	mh := MultiHooks(plain, hand, gr)

	from, to := &Agent{Name: "a"}, &Agent{Name: "b"}
	mh.(HandoffHook).OnHandoff(context.Background(), from, to, "r1")
	mh.(GuardrailHook).OnGuardrail(context.Background(), from, "r1", StageInput, "g", GuardrailResult{Tripwire: true}, nil, 0)
	if len(hand.handoffs) != 1 || hand.handoffs[0] != "a->b" {
		t.Fatalf("handoffRecorder handoffs = %v", hand.handoffs)
	}
	if strings.Join(gr.verdicts, ",") != "input:g:trip" {
		t.Fatalf("guardrailRecorder verdicts = %v", gr.verdicts)
	}

	// Integration: a tripping run reaches every hook, while only the
	// extensions see guardrail and handoff events.
	plain2, hand2, gr2 := &recorder{}, &handoffRecorder{recorder: &recorder{}}, &guardrailRecorder{recorder: &recorder{}}
	a := &Agent{Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{tripInput("block")}}
	if _, err := (&Runner{Hooks: MultiHooks(plain2, hand2, gr2)}).Run(context.Background(), a, "x"); !errors.Is(err, ErrGuardrailTripwire) {
		t.Fatalf("err = %v", err)
	}
	for i, r := range []*recorder{plain2, hand2.recorder, gr2.recorder} {
		if len(r.events) == 0 || r.events[0] != "run_start" {
			t.Fatalf("hook %d events = %v", i, r.events)
		}
	}
	if strings.Contains(strings.Join(plain2.events, ","), "guardrail") {
		t.Fatalf("plain hooks saw guardrail events: %v", plain2.events)
	}
	if strings.Join(gr2.verdicts, ",") != "input:block:trip" {
		t.Fatalf("gr2 verdicts = %v", gr2.verdicts)
	}
}

func collectStreamTypes(t *testing.T, run *StreamRun) []StreamEventType {
	t.Helper()
	var types []StreamEventType
	for ev := range run.Events {
		types = append(types, ev.Type)
	}
	return types
}

func countTerminals(types []StreamEventType) int {
	n := 0
	for _, ty := range types {
		if ty == StreamFinalOutput || ty == StreamRunError {
			n++
		}
	}
	return n
}

func TestStreamInputGuardrailTripwire(t *testing.T) {
	a := &Agent{Name: "a", Model: testutil.NewScripted(), InputGuardrails: []InputGuardrail{tripInput("block")}}
	run := NewRunner().RunStream(context.Background(), a, "x")
	types := collectStreamTypes(t, run)
	want := []StreamEventType{StreamRunStarted, StreamRunError}
	if strings.Join(stringify(types), ",") != strings.Join(stringify(want), ",") {
		t.Fatalf("events = %v, want %v", types, want)
	}
	if countTerminals(types) != 1 {
		t.Fatalf("terminal events = %d, want 1", countTerminals(types))
	}
	_, err := run.Result()
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) || trip.Stage != StageInput {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamOutputGuardrailTripwire(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("he"), testutil.TextChunk("llo")},
		FinishReason: "stop",
	})
	a := &Agent{Name: "a", Model: m, OutputGuardrails: []OutputGuardrail{tripOutput("leak")}}
	run := NewRunner().RunStream(context.Background(), a, "x")
	types := collectStreamTypes(t, run)
	want := []StreamEventType{StreamRunStarted, StreamTextDelta, StreamTextDelta, StreamRunError}
	if strings.Join(stringify(types), ",") != strings.Join(stringify(want), ",") {
		t.Fatalf("events = %v, want %v", types, want)
	}
	if countTerminals(types) != 1 {
		t.Fatalf("terminal events = %d, want 1", countTerminals(types))
	}
	_, err := run.Result()
	var trip *GuardrailTripwireError
	if !errors.As(err, &trip) || trip.Stage != StageOutput || trip.Guardrail != "leak" {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamOutputGuardrailPass(t *testing.T) {
	m := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("hello")},
		FinishReason: "stop",
	})
	a := &Agent{Name: "a", Model: m, OutputGuardrails: []OutputGuardrail{{
		Name: "check", Guardrail: func(ctx context.Context, a *Agent, res *RunResult) (GuardrailResult, error) {
			return GuardrailResult{}, nil
		},
	}}}
	run := NewRunner().RunStream(context.Background(), a, "x")
	types := collectStreamTypes(t, run)
	if types[len(types)-1] != StreamFinalOutput || countTerminals(types) != 1 {
		t.Fatalf("events = %v", types)
	}
	res, err := run.Result()
	if err != nil || res.Output != "hello" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func stringify(types []StreamEventType) []string {
	out := make([]string, len(types))
	for i, ty := range types {
		out[i] = string(ty)
	}
	return out
}
