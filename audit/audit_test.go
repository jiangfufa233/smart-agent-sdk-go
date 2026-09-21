package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/handoff"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
)

type qArgs struct {
	Q string `json:"q"`
}

func parseRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func msgOf(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

func allMsgs(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func TestAuditFullRun(t *testing.T) {
	var buf bytes.Buffer
	lg := NewSlog(slog.New(slog.NewJSONHandler(&buf, nil)))

	search, err := tool.NewFunction("search", "search the web",
		func(ctx context.Context, in qArgs) (string, error) { return "result text", nil })
	if err != nil {
		t.Fatal(err)
	}
	m := testutil.NewScripted(
		testutil.ToolCallStep("t1", "search", `{"q":"x"}`),
		testutil.TextStep("final answer"),
	)
	a := &agent.Agent{
		ID:    "audited",
		Name:  "audited",
		Model: m,
		Tools: []tool.Tool{search},
		InputGuardrails: []agent.InputGuardrail{{Name: "in", Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
			return agent.GuardrailResult{}, nil
		}}},
		OutputGuardrails: []agent.OutputGuardrail{{Name: "out", Guardrail: func(ctx context.Context, a *agent.Agent, res *agent.RunResult) (agent.GuardrailResult, error) {
			return agent.GuardrailResult{}, nil
		}}},
	}
	if _, err := (&agent.Runner{Hooks: lg}).Run(context.Background(), a, "user input"); err != nil {
		t.Fatal(err)
	}

	recs := parseRecords(t, &buf)
	for _, msg := range []string{"run_started", "llm_call", "llm_response", "tool_call", "tool_result", "guardrail", "run_ended"} {
		if msgOf(recs, msg) == nil {
			t.Fatalf("no %s record in:\n%s", msg, buf.String())
		}
	}

	rs := msgOf(recs, "run_started")
	if rs["input"] != "user input" || rs["agent"] != "audited" || rs["run_id"] == "" {
		t.Fatalf("run_started = %v", rs)
	}
	lc := msgOf(recs, "llm_call")
	msgs, _ := lc["messages"].(string)
	if !strings.Contains(msgs, "user input") {
		t.Fatalf("llm_call messages missing raw input: %v", lc)
	}
	tc := msgOf(recs, "tool_call")
	if tc["tool"] != "search" || tc["args"] != `{"q":"x"}` {
		t.Fatalf("tool_call = %v", tc)
	}
	tr := msgOf(recs, "tool_result")
	if tr["result"] != "result text" {
		t.Fatalf("tool_result = %v", tr)
	}
	grs := allMsgs(recs, "guardrail")
	if len(grs) != 2 || grs[0]["stage"] != "input" || grs[1]["stage"] != "output" {
		t.Fatalf("guardrail records = %v", grs)
	}
	if grs[0]["tripwire"] != false || grs[0]["guardrail"] != "in" {
		t.Fatalf("input guardrail record = %v", grs[0])
	}
	re := msgOf(recs, "run_ended")
	if re["output"] != "final answer" || re["final_agent"] != "audited" {
		t.Fatalf("run_ended = %v", re)
	}
}

func TestAuditTripwireAndFailure(t *testing.T) {
	var buf bytes.Buffer
	lg := NewSlog(slog.New(slog.NewJSONHandler(&buf, nil)))
	a := &agent.Agent{ID: "audit", Name: "audit", Model: testutil.NewScripted(testutil.TextStep("unused")),
		InputGuardrails: []agent.InputGuardrail{{Name: "block", Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
			return agent.GuardrailResult{Tripwire: true, Info: "policy X"}, nil
		}}}}
	_, err := (&agent.Runner{Hooks: lg}).Run(context.Background(), a, "bad input")
	if !strings.Contains(err.Error(), "tripped") {
		t.Fatalf("err = %v", err)
	}

	recs := parseRecords(t, &buf)
	g := msgOf(recs, "guardrail")
	if g["tripwire"] != true || g["info"] != "policy X" {
		t.Fatalf("guardrail record = %v", g)
	}
	re := msgOf(recs, "run_ended")
	if re == nil || !strings.Contains(re["error"].(string), "tripped") {
		t.Fatalf("run_ended = %v", re)
	}
	if msgOf(recs, "llm_call") != nil {
		t.Fatal("tripwire fired but a model call was recorded")
	}
}

func TestAuditToolFailure(t *testing.T) {
	var buf bytes.Buffer
	lg := NewSlog(slog.New(slog.NewJSONHandler(&buf, nil)))

	search, err := tool.NewFunction("search", "fails",
		func(ctx context.Context, in qArgs) (string, error) { return "", context.DeadlineExceeded })
	if err != nil {
		t.Fatal(err)
	}
	m := testutil.NewScripted(
		testutil.ToolCallStep("t1", "search", `{"q":"x"}`),
		testutil.TextStep("recovered"),
	)
	a := &agent.Agent{ID: "a", Name: "a", Model: m, Tools: []tool.Tool{search}}
	if _, err := (&agent.Runner{Hooks: lg}).Run(context.Background(), a, "hi"); err != nil {
		t.Fatal(err)
	}

	recs := parseRecords(t, &buf)
	tr := msgOf(recs, "tool_result")
	if tr == nil || tr["error"] == nil || tr["result"] == "" {
		t.Fatalf("tool_result = %v, want error and fed-back text", tr)
	}
}

func TestAuditDelegationCorrelatesChildRun(t *testing.T) {
	var buf bytes.Buffer
	lg := NewSlog(slog.New(slog.NewJSONHandler(&buf, nil)))
	runner := &agent.Runner{Hooks: lg}
	child := &agent.Agent{ID: "researcher", Name: "Researcher", Model: testutil.NewScripted(testutil.TextStep("research complete"))}
	delegated, err := handoff.Delegate(child, runner)
	if err != nil {
		t.Fatal(err)
	}
	manager := &agent.Agent{
		ID:   "manager",
		Name: "Manager",
		Model: testutil.NewScripted(
			testutil.ToolCallStep("delegate", delegated.Spec().Function.Name, `{"message":"research"}`),
			testutil.TextStep("final answer"),
		),
		Tools: []tool.Tool{delegated},
	}
	if _, err := runner.Run(context.Background(), manager, "investigate"); err != nil {
		t.Fatal(err)
	}

	var parent, delegatedChild map[string]any
	for _, rec := range allMsgs(parseRecords(t, &buf), "run_started") {
		switch rec["agent"] {
		case manager.Name:
			parent = rec
		case child.Name:
			delegatedChild = rec
		}
	}
	if parent == nil || delegatedChild == nil {
		t.Fatalf("run records = %s", buf.String())
	}
	parentTask, _ := parent["task_id"].(string)
	parentRoot, _ := parent["root_run_id"].(string)
	parentSource, _ := parent["source_run_id"].(string)
	childTask, _ := delegatedChild["task_id"].(string)
	childParent, _ := delegatedChild["parent_task_id"].(string)
	childRoot, _ := delegatedChild["root_run_id"].(string)
	childSource, _ := delegatedChild["source_run_id"].(string)
	if parentTask == "" || parentTask != parentRoot || parentTask != parentSource || childTask == "" || childTask == childSource || childParent != parentTask || childRoot != parentRoot || childSource == "" {
		t.Fatalf("parent=%v child=%v", parent, delegatedChild)
	}
}
