// Command offline demonstrates the full agent loop without any network access
// by using the scripted fake models from the testutil package. It doubles as
// a smoke test for the SDK: schema generation, the tool-call loop, handoffs
// (both patterns), structured output, skills, streaming, guardrails, audit
// logging, session persistence and history compression.
//
// Usage:
//
//	go run ./examples/offline
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/audit"
	"github.com/jiangfufa233/smart-agent-sdk-go/guardrail"
	"github.com/jiangfufa233/smart-agent-sdk-go/handoff"
	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/session"
	"github.com/jiangfufa233/smart-agent-sdk-go/skill"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

type weatherArgs struct {
	City string `json:"city" desc:"City name to get the weather for"`
	Days int    `json:"days,omitempty" desc:"Forecast horizon in days"`
}

func main() {
	ctx := context.Background()

	// 1. Reflection-based JSON schema generation.
	weatherTool, err := tool.NewFunction("get_weather", "Return the current weather for a city.",
		func(ctx context.Context, in weatherArgs) (string, error) {
			return fmt.Sprintf(`{"city":%q,"temperature_c":21}`, in.City), nil
		})
	must(err)
	fmt.Println("generated schema:", string(weatherTool.Spec().Function.Parameters))

	// 2. Runner loop: tool call -> tool result -> final answer.
	weatherModel := testutil.NewScripted(
		testutil.ToolCallStep("call_1", "get_weather", `{"city":"Beijing"}`),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != model.RoleTool || last.Content == "" {
				return nil, fmt.Errorf("expected tool result message, got %+v", last)
			}
			return testutil.TextStep("Sunny, 21C in Beijing (via " + last.Content + ").").Resp, nil
		}},
	)
	a := &agent.Agent{
		Name:         "weather-agent",
		Instructions: "You are a helpful assistant.",
		Model:        weatherModel,
		Tools:        []tool.Tool{weatherTool},
	}
	res, err := agent.NewRunner().Run(ctx, a, "What's the weather like in Beijing?")
	must(err)
	fmt.Println("agent>", res.Output)
	fmt.Println("conversation turns:", len(res.Messages))

	// 3. Handoff: wrap a sub-agent as a tool.
	researcherModel := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		in := req.Messages[len(req.Messages)-1].Content
		return testutil.TextStep("researcher handled: " + in).Resp, nil
	}})
	sub := &agent.Agent{Name: "researcher", Model: researcherModel}
	ht, err := handoff.AsTool(sub, nil)
	must(err)
	out, err := ht.Run(ctx, `{"message":"look this up"}`)
	must(err)
	fmt.Println("handoff result>", out)

	// 4. First-class handoff: a transfer_to_* tool call continues the run
	// with the target agent in the same conversation.
	specialist := &agent.Agent{
		Name:         "Research Specialist",
		Instructions: "You are a research specialist.",
		Model: testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			if req.Messages[0].Role != model.RoleSystem || req.Messages[0].Content != "You are a research specialist." {
				return nil, fmt.Errorf("system prompt not switched: %+v", req.Messages[0])
			}
			return testutil.TextStep("Research done: 3 papers found.").Resp, nil
		}}),
	}
	supervisor := &agent.Agent{
		Name:         "supervisor",
		Instructions: "You route research questions.",
		Model: testutil.NewScripted(
			testutil.ToolCallStep("call_2", "transfer_to_research_specialist", "{}"),
		),
		Handoffs: []agent.Handoff{handoff.New(specialist)},
	}
	res2, err := agent.NewRunner().Run(ctx, supervisor, "Find papers on agent frameworks.")
	must(err)
	fmt.Printf("handoff> transfers=%v final_agent=%s output=%q\n",
		res2.Transfers, res2.Agent.Name, res2.Output)

	// 5. Structured output: RunTyped injects a json_schema response format
	// derived from the Go type and decodes the final answer into it.
	type weatherReport struct {
		City        string  `json:"city"`
		Temperature float64 `json:"temperature" desc:"Temperature in Celsius"`
	}
	typed, err := agent.RunTyped[weatherReport](ctx, agent.NewRunner(),
		&agent.Agent{
			Name:  "structured",
			Model: testutil.NewScripted(testutil.TextStep("```json\n{\"city\":\"Beijing\",\"temperature\":21}\n```")),
		}, "weather in Beijing?")
	must(err)
	fmt.Printf("typed> %+v (schema name %q)\n", typed.Value,
		typed.Result.Agent.Settings.ResponseFormat.JSONSchema.Name)

	// 6. Skills: parse SKILL.md frontmatter and expose as a tool.
	dir, err := os.MkdirTemp("", "skilldemo")
	must(err)
	defer func() { _ = os.RemoveAll(dir) }()
	must(os.WriteFile(dir+"/SKILL.md", []byte(
		"---\nname: pdf-extract\ndescription: Extract text from PDF files\n---\n# Steps\n1. Parse the PDF\n2. Emit text\n"), 0o644))
	skills, err := skill.LoadDir(dir)
	must(err)
	st, err := skills[0].Tool()
	must(err)
	body, err := st.Run(ctx, "{}")
	must(err)
	fmt.Printf("skill %q loaded, body:\n%s\n", skills[0].Name, body)

	// 7. Streaming: incremental events via Runner.RunStream.
	streamModel := testutil.NewScriptedStream(testutil.StreamStep{
		Deltas:       []model.StreamEvent{testutil.TextChunk("Hel"), testutil.TextChunk("lo"), testutil.TextChunk(", world!")},
		FinishReason: "stop",
		Usage:        model.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7},
	})
	run := agent.NewRunner().RunStream(ctx, &agent.Agent{Name: "streamer", Model: streamModel}, "Greet the world.")
	for ev := range run.Events {
		switch ev.Type {
		case agent.StreamTextDelta:
			fmt.Print(ev.Text)
		case agent.StreamFinalOutput:
			fmt.Printf("\nstream> done, finish=%s, usage=%d tokens\n", ev.FinishReason, ev.Usage.TotalTokens)
		case agent.StreamRunError:
			fmt.Println("stream> error:", ev.Err)
		}
	}
	if _, err := run.Result(); err != nil {
		must(err)
	}

	// 8. Guardrails: tripwires fail the run. An input tripwire fires before
	// any model call — the scripted model below is never reached.
	guarded := &agent.Agent{
		Name:  "guarded",
		Model: testutil.NewScripted(testutil.TextStep("this must never be reached")),
		InputGuardrails: []agent.InputGuardrail{
			guardrail.DenyPatterns("secrets",
				regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
				regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)),
			guardrail.MaxLength(500),
		},
	}
	if _, err := agent.NewRunner().Run(ctx, guarded, "my API key is sk-abcdefghij01234567890"); err == nil {
		fmt.Fprintln(os.Stderr, "error: expected the input guardrail to trip")
		os.Exit(1)
	} else {
		var trip *agent.GuardrailTripwireError
		if !errors.As(err, &trip) {
			must(err)
		}
		fmt.Printf("guardrail> %s guardrail %q tripped, info=%v\n", trip.Stage, trip.Guardrail, trip.Info)
	}

	// A clean run with output guardrails and the full-content audit log on
	// stdout. Combine with agent.SlogHooks via agent.MultiHooks if you also
	// want the privacy-preserving operational log.
	audited := &agent.Agent{
		Name:  "audited",
		Model: testutil.NewScripted(testutil.TextStep("Here is the safe answer.")),
		OutputGuardrails: []agent.OutputGuardrail{{
			Name: "no-placeholders",
			Guardrail: func(ctx context.Context, a *agent.Agent, r *agent.RunResult) (agent.GuardrailResult, error) {
				if strings.Contains(r.Output, "TODO") {
					return agent.GuardrailResult{Tripwire: true, Info: "unfinished output"}, nil
				}
				return agent.GuardrailResult{}, nil
			},
		}},
	}
	auditRunner := &agent.Runner{Hooks: audit.NewSlog(slog.New(slog.NewJSONHandler(os.Stdout, nil)))}
	res3, err := auditRunner.Run(ctx, audited, "Say something safe.")
	must(err)
	fmt.Println("audit> output:", res3.Output)

	// 9. Sessions: pass a Session instead of res.Messages — the history is
	// loaded before the run and the new messages are written back after it
	// succeeds. The system prompt is never stored.
	chat := &agent.Agent{
		Name:         "chat",
		Instructions: "You are a helpful assistant.",
		Model:        chatModel{},
	}
	sess := session.NewInMemory()
	sessionRunner := agent.NewRunner()
	if _, err := sessionRunner.RunWithSession(ctx, chat, sess, "first hello"); err != nil {
		must(err)
	}
	res4, err := sessionRunner.RunWithSession(ctx, chat, sess, "second hello")
	must(err)
	fmt.Println("session>", res4.Output)

	// History compression is view-level: the session stays lossless, the
	// model sees less. A SlidingWindow keeps the system prompt plus the
	// most recent messages; a Summarizer folds the old tail into a rolling
	// summary (one model call per fold, with hysteresis in between).
	items, err := sess.GetItems(ctx, 0)
	must(err)
	fmt.Println("session items stored:", len(items))

	compressing := agent.NewRunner()
	compressing.Compressor = session.NewSlidingWindow(4)
	if _, err := compressing.RunWithSession(ctx, chat, sess, "third hello"); err != nil {
		must(err)
	}
	sum := session.NewSummarizer(testutil.NewScripted(testutil.TextStep("The user greeted the assistant.")))
	sum.High, sum.Low = 4, 2
	compressing.Compressor = sum
	if _, err := compressing.RunWithSession(ctx, chat, sess, "summarize me"); err != nil {
		must(err)
	}
	items, err = sess.GetItems(ctx, 0)
	must(err)
	fmt.Println("session items after compression runs:", len(items), "(storage stays lossless)")
}

// chatModel answers every call with a description of what it received,
// which makes the session and compression effects visible in the output.
type chatModel struct{}

func (chatModel) Chat(ctx context.Context, req *model.Request) (*model.Response, error) {
	return testutil.TextStep(fmt.Sprintf("you sent %d message(s), last: %q",
		len(req.Messages), req.Messages[len(req.Messages)-1].Content)).Resp, nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
