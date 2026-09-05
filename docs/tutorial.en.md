# smart-agent-sdk-go Tutorial

From zero to production: build a Go agent step by step. Each lesson answers three questions: **when do you need it, how do you write it, what should you watch out for**.

> 中文版：[tutorial.md](tutorial.md)（结构与代码块完全一致）
>
> API reference: [pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go](https://pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go)

| Lesson | Topic | When you need it |
|--------|-------|------------------|
| [0](#0-setup) | Setup: environment and model endpoints | Before starting |
| [1](#1-your-first-agent) | Your first agent | First conversation working |
| [2](#2-multi-turn-sessions) | Multi-turn sessions | A chatbot that remembers context |
| [3](#3-history-compression) | History compression | Conversations outgrow the context window |
| [4](#4-function-tools) | Function tools | Let the model query databases, call APIs |
| [5](#5-streaming) | Streaming | Typewriter output, live progress |
| [6](#6-structured-output) | Structured output | Go structs instead of free text |
| [7](#7-orchestration-handoffs) | Orchestration (handoffs) | Multiple agents cooperating |
| [8](#8-guardrails) | Guardrails | Blocking sensitive input, moderating output |
| [9](#9-observability-hooks-audit-tracing) | Observability | Logs, audit trails, tracing |
| [10](#10-mcp-servers) | MCP servers | Reusing the MCP tool ecosystem |
| [11](#11-resilience-retry-timeout-rate-limit-fallback) | Resilience | Production networks and quotas |
| [12](#12-testing-your-agent) | Testing | Testing agent logic without tokens |
| [13](#13-built-in-security-tools-sandbox) | Built-in security tools | Letting the agent run commands and read files safely |
| [Appendix A](#appendix-aarchitecture-overview) | Architecture overview | Understanding the internals |
| [Appendix B](#appendix-bdifferences-from-openai-agents-python) | Differences from openai-agents-python | Migrating from the Python version |

## 0. Setup

**Requirements**: Go 1.25+.

```bash
go get github.com/jiangfufa233/smart-agent-sdk-go
```

**Model endpoint**: the SDK speaks the OpenAI Chat Completions protocol, so any compatible endpoint works — OpenAI, vLLM, Ollama, Qwen services:

```go
// OpenAI
m := openai.New(openai.Config{
    APIKey:       os.Getenv("OPENAI_API_KEY"),
    DefaultModel: "gpt-4o-mini",
})

// Local / private endpoint (Ollama shown)
m := openai.New(openai.Config{
    APIKey:       "ollama", // most local endpoints don't check; a placeholder is fine
    BaseURL:      "http://localhost:11434/v1",
    DefaultModel: "qwen2.5:7b",
})
```

**Running the snippets**: lesson 1 gives a complete `main.go`; later lessons only show key fragments — paste them into that skeleton (inside `main`). Add standard-library imports as needed; SDK packages all come from `github.com/jiangfufa233/smart-agent-sdk-go/...`.

**Runnable examples** live in the repo's `examples/` directory: `chat` (multi-turn dialogue), `tools` (tool calls), `mcp` (MCP integration), `offline` (offline smoke test covering every feature, no network needed).

## 1. Your first agent

An agent = a system prompt (`Instructions`) + a model (`Model`); a run = `Runner.Run`. This is a complete, runnable program:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/jiangfufa233/smart-agent-sdk-go/agent"
    "github.com/jiangfufa233/smart-agent-sdk-go/model/openai"
)

func main() {
    ctx := context.Background()

    m := openai.New(openai.Config{
        APIKey:       os.Getenv("OPENAI_API_KEY"),
        DefaultModel: "gpt-4o-mini",
    })

    myAgent := &agent.Agent{
        Name:         "assistant",
        Instructions: "You are a concise assistant.",
        Model:        m,
    }

    res, err := agent.NewRunner().Run(ctx, myAgent, "Explain what an agent is in one sentence.")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(res.Output)
}
```

```bash
export OPENAI_API_KEY=sk-...
go run main.go
```

**What's inside `RunResult`**:

| Field | Meaning |
|-------|---------|
| `Output` | The final textual answer |
| `FinalMessage` | The model message that ended the run |
| `Messages` | The full conversation (system, tool calls and results); pass it to `RunWithHistory` to continue |
| `ToolErrors` | Tool invocations that failed during the run (their error text was fed back to the model, lesson 4) |
| `Usage` | Token consumption accumulated across all model calls |
| `Duration` / `RunID` | Wall time and the run's correlation ID (logs, audit) |
| `Agent` / `Transfers` | The agent that produced the answer and the handoff path (lesson 7) |

**Key points**:
- `Runner` is stateless and reusable — one runner can serve many agents.
- Each model call consumes one turn; `Runner.MaxTurns` defaults to 10 to prevent endless tool loops.
- Never ignore `err`: model-side failures are typed `*model.ModelError` (lesson 11); guardrail and session errors have their own types too.

## 2. Multi-turn sessions

**When you need it**: the chatbot must remember what was said before.

Without a session you shuttle history by hand:

```go
res1, err := agent.NewRunner().Run(ctx, myAgent, "My name is Jiang and I love Go.")
res2, err := agent.NewRunner().RunWithHistory(ctx, myAgent, res1.Messages, "What's my name?")
```

`RunWithSession` automates loading and write-back:

```go
sess := session.NewInMemory()
r := agent.NewRunner()

_, err := r.RunWithSession(ctx, myAgent, sess, "My name is Jiang and I love Go.")
res, err := r.RunWithSession(ctx, myAgent, sess, "What's my name?")
fmt.Println(res.Output) // the model sees the previous turn
```

**Three stores**, one interface (`GetItems` / `AddItems` / `Clear`):

```go
sess := session.NewInMemory()               // in-process, lost on restart
sess = session.NewFile("chat.jsonl")        // append-only JSONL, survives restarts
store, err := session.NewSQLiteStore("chats.db") // one DB file, many sessions
sess = store.Get("user-42")                 // keyed view, ids are isolated
```

**Semantics** (worth 30 seconds — each rule came from a real failure mode):
- The run's new messages (the user input plus generated assistant/tool messages) are written back **only on success**; **failed runs never persist** — no half-finished conversations leak into the next turn.
- A write-back failure fails the run: silently losing context is worse than failing loudly.
- The system prompt is never stored — it is `Agent` configuration, prepended fresh each run. Changing `Instructions` doesn't require clearing the session.
- Sessions store the **full, lossless** history; the view sent to the model can be compressed (lesson 3).
- Transcripts produced by handoffs go into the session as-is (the conversation can continue under the new agent, lesson 7).

**Gotcha**: the SQLite DB file can be shared by multiple processes (busy_timeout and WAL are configured), but it is a single-file store — not a network database.

## 3. History compression

**When you need it**: after hundreds of turns the history outgrows the model's context window.

Give the `Runner` a compressor. Compression is **view-level**: the session stays lossless; only the history sent to the model shrinks, and `res.Messages` reflects exactly what the model saw.

```go
// Option 1: sliding window — keep system + the most recent N messages, zero cost
r := &agent.Runner{Compressor: session.NewSlidingWindow(40)}

// Option 2: rolling summary — fold old messages into one summary (use a cheap model)
sum := session.NewSummarizer(cheapModel) // cheapModel can be another openai instance
sum.High, sum.Low = 50, 20               // fold above 50 non-system messages, keep 20 verbatim
r := &agent.Runner{Compressor: sum}
```

How `Summarizer` saves money:
- **Hysteresis**: after a compression the view drops to `Low`; it is only folded again when it grows back to `High` — not once per turn;
- **Incremental folding**: when it triggers, only the **newly added messages** are folded into the existing summary;
- Summarizer model calls scale as ≈ `(len−High)/(High−Low)` instead of one per turn.

**Key points**:
- The summary cache lives on the `Summarizer` instance: **reusing the same Runner reuses the cache**; a new Runner re-summarizes from scratch.
- A larger `High−Low` gap means fewer summarizer calls but coarser folds. 50/20 is a solid start.
- Compression only changes the view; audit (lesson 9) records the compressed request view, while the session always keeps the full transcript.

## 4. Function tools

**When you need it**: the model should query a database or call an internal API — the SDK runs the whole loop: model requests tool → execute → feed result back → model continues.

Write a plain Go function; the args struct is the JSON Schema:

```go
type weatherArgs struct {
    City string `json:"city" desc:"City name, e.g. Beijing"`
    Days int    `json:"days" desc:"Forecast horizon in days"`
}

weatherTool, err := tool.NewFunction("get_weather", "Look up a city's weather forecast",
    func(ctx context.Context, in weatherArgs) (string, error) {
        return fmt.Sprintf("%s: sunny, high 21C (%d days)", in.City, in.Days), nil
    })
if err != nil {
    log.Fatal(err)
}

myAgent.Tools = []tool.Tool{weatherTool}
res, err := agent.NewRunner().Run(ctx, myAgent, "What's the weather in Beijing for the next 3 days?")
fmt.Println(res.Output) // the model already called get_weather and answered from the result
```

The SDK reflects `weatherArgs` into JSON Schema (`json` tags name fields, `desc` tags describe them, required is inferred) and sends it to the model. Function signatures: `func(in T) (string, error)` or a variant with a leading `ctx`.

**Tool failures don't fail the run**: when a tool returns an error (or panics), the error text is fed back to the model as the tool result so it can adapt (retry with different args, apologize, …), and the failure is recorded in `res.ToolErrors`:

```go
for _, te := range res.ToolErrors {
    log.Printf("tool %s args %s failed: %v", te.Tool, te.Arguments, te.Err)
}
```

**Key points**:
- The tool's name and description are the model's only basis for deciding when to call it — write them carefully.
- Tool output occupies context; oversized returns are truncated (`Runner.MaxToolOutputBytes`, default 512 KiB).
- `Runner.ToolTimeout` bounds each tool execution independently.

## 5. Streaming

**When you need it**: typewriter output; live visibility into tool calls and handoffs.

Model clients that implement the streaming interface (the return value of `openai.New` does) work with `RunStream`, which returns an event stream:

```go
run := agent.NewRunner().RunStream(ctx, myAgent, "Tell me a 100-word story")
for ev := range run.Events {
    switch ev.Type {
    case agent.StreamTextDelta: // incremental text
        fmt.Print(ev.Text)
    case agent.StreamToolCallStarted, agent.StreamToolCallArgs, agent.StreamToolResult:
        // live tool progress (ev.Call / ev.Result / ev.ToolErr)
    case agent.StreamHandoff:
        fmt.Printf("\n[handoff %s -> %s]\n", ev.FromAgent, ev.ToAgent)
    case agent.StreamFinalOutput: // terminal: full text + usage
        fmt.Printf("\n[done, %d tokens]\n", ev.Usage.TotalTokens)
    case agent.StreamRunError: // terminal: failure
        log.Printf("run failed: %v", ev.Err)
    }
}
res, err := run.Result()
```

Full event set: `StreamRunStarted` / `StreamTextDelta` / `StreamToolCallStarted` / `StreamToolCallArgs` / `StreamToolCallFinished` / `StreamToolResult` / `StreamHandoff` / `StreamFinalOutput` / `StreamRunError`.

**Key points**:
- **Exactly one terminal event**: `StreamFinalOutput` or `StreamRunError`, followed by channel close. Two correct consumption patterns: range until close, or `run.Wait()` (drains and returns the result).
- Only want the outcome? `res, err := agent.NewRunner().RunStream(ctx, a, in).Wait()` — one line.
- If the model doesn't implement streaming, the run automatically degrades to a plain call (the full answer arrives as a single TextDelta); your code doesn't change.
- Combine with sessions: `RunStreamWithSession(ctx, a, sess, input)`; the write-back happens before the terminal event, so the single-terminal invariant holds.

**Gotcha**: starting a run and then neither consuming `Events` nor calling `Wait()` stalls the run once the event buffer fills (backpressure by design) — drain unused streams or cancel the ctx.

## 6. Structured output

**When you need it**: downstream code wants data (forms, tickets, extractions), not prose.

```go
type Report struct {
    City   string  `json:"city" desc:"City name"`
    TempC  float64 `json:"temp_c" desc:"Temperature in Celsius"`
    Sunny  bool    `json:"sunny" desc:"Whether it is sunny"`
}

typed, err := agent.RunTyped[Report](ctx, agent.NewRunner(), myAgent, "Current weather in Beijing?")
if err != nil {
    var se *agent.StructuredOutputError
    if errors.As(err, &se) {
        log.Fatalf("model did not emit valid JSON, raw: %s", se.Raw)
    }
    log.Fatal(err)
}
fmt.Printf("%s %.1fC sunny=%v\n", typed.Value.City, typed.Value.TempC, typed.Value.Sunny)
```

`RunTyped[Report]` reflects the Go type into a JSON Schema, injects it into the request (without mutating the original `Agent`; an existing `ResponseFormat` is respected), and decodes the final output into `typed.Value`; `typed.Result` is the underlying run result (usage, transfers, …). JSON wrapped in Markdown code fences is tolerated.

**Key points**:
- `desc` tags feed the schema and directly affect extraction quality — write them.
- Decode failures are typed `*agent.StructuredOutputError` (carrying the raw text); match with `errors.As` to retry or escalate.

## 7. Orchestration (handoffs)

**When you need it**: one agent can't do it all — triage hands research questions to a specialist, support hands refunds to billing.

**First-class handoffs**: each handoff is exposed as a `transfer_to_<name>` no-arg tool; when the model calls it, **the same conversation** continues under the target agent — tool surface, sampling settings and system prompt all switch, history fully shared:

```go
researcher := &agent.Agent{
    Name:         "researcher",
    Instructions: "You are a research specialist; dig deep and give structured conclusions.",
    Model:        m,
}
triage := &agent.Agent{
    Name:         "triage",
    Instructions: "You triage: answer simple questions; hand research questions to researcher.",
    Model:        m,
    Handoffs:     []agent.Handoff{handoff.New(researcher)},
}

res, err := agent.NewRunner().Run(ctx, triage, "Research best practices for Go generics")
fmt.Println(res.Transfers)   // [researcher]
fmt.Println(res.Agent.Name)  // researcher — it produced the final answer
```

**Nested delegation (agent-as-tool)**: the subtask runs as its own run; only the final answer returns to the caller:

```go
subTool, err := handoff.AsTool(researcher, agent.NewRunner())
triage.Tools = append(triage.Tools, subTool)
```

| Need | Use |
|------|-----|
| The conversation should **change hands** (the user keeps talking to the new agent) | Handoff |
| You only need the **subtask's result** (the caller keeps talking with the result in hand) | AsTool |

**Key points**:
- The target agent must have a `Model` (validation rejects nil); a handoff tool colliding with a regular tool name is an error.
- `MaxTurns` bounds model calls **across all** agents, preventing A↔B ping-pong.
- In streaming, handoffs emit `StreamHandoff` events (`FromAgent`/`ToAgent`); implementing the optional `agent.HandoffHook` also surfaces them as callbacks.
- Post-handoff transcripts enter the session as-is (lesson 2), so the conversation continues under the new agent.

## 8. Guardrails

**When you need it**: user input must not contain secrets, model output must not leak sensitive content. A tripped guardrail fails the entire run.

```go
myAgent.InputGuardrails = []agent.InputGuardrail{
    guardrail.MaxLength(2000), // reject over-long input outright
    guardrail.DenyPatterns("secrets", regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)),
}
myAgent.OutputGuardrails = []agent.OutputGuardrail{{
    Name: "no-secrets",
    Guardrail: func(ctx context.Context, a *agent.Agent, res *agent.RunResult) (agent.GuardrailResult, error) {
        if strings.Contains(res.Output, "sk-") {
            return agent.GuardrailResult{Tripwire: true, Info: "output may leak a credential"}, nil
        }
        return agent.GuardrailResult{}, nil
    },
}}

_, err := agent.NewRunner().Run(ctx, myAgent, "Check this key: sk-abc123...")
var trip *agent.GuardrailTripwireError
if errors.As(err, &trip) {
    fmt.Printf("blocked: stage=%s guardrail=%s info=%v\n", trip.Stage, trip.Guardrail, trip.Info)
}
```

**Semantics**:
- Input guardrails run **before the first model call**, concurrently, and all of them finish (a tripwire or a guardrail error fails the run — fail-closed). A trip means **zero model calls** and zero tokens.
- Output guardrails belong to the agent that **produced the final answer** (after a handoff, the specialist's) and run before the result is published; in streaming, text deltas may already have been consumed — `StreamRunError` is authoritative.
- `guardrail.DenyPatterns`' `Info` records only the pattern and match offsets, **never the matched content** — so real secrets don't get copied into logs. Hold your own guardrails to the same standard.

**Model-moderation guardrail**: use another model as a guardrail (a full example lives in the package docs):

```go
moderator := model.WithTimeout(openai.New(moderatorCfg), 5*time.Second) // always a timeout — don't let a guardrail stall the run
myAgent.InputGuardrails = append(myAgent.InputGuardrails, agent.InputGuardrail{
    Name: "moderation",
    Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
        res, err := moderator.Chat(ctx, &model.Request{Messages: []model.Message{
            {Role: model.RoleUser, Content: "Is the following content violating? Answer yes/no only: " + input},
        }})
        if err != nil {
            return agent.GuardrailResult{}, err // a guardrail error = fail-closed
        }
        return agent.GuardrailResult{Tripwire: strings.HasPrefix(res.Message.Content, "yes")}, nil
    },
})
```

## 9. Observability (hooks, audit, tracing)

**When you need it**: incidents need replay; compliance needs records.

Observability has three layers, use what you need:

```go
f, err := os.OpenFile("audit.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
if err != nil {
    log.Fatal(err)
}

r := &agent.Runner{
    // one-line structured logging: identifiers and sizes only, no raw content or keys
    Hooks: agent.SlogHooks(slog.Default()),
    // tracing: spans for the run, each model call, each tool call
    Tracer: tracing.NewSlog(slog.Default()),
}

// full audit: coexists with SlogHooks via fan-out
auditLog := audit.NewSlog(slog.New(slog.NewJSONHandler(f, nil)))
r.Hooks = agent.MultiHooks(agent.SlogHooks(slog.Default()), auditLog)
```

| Layer | Implementation | Records | Best for |
|-------|----------------|---------|----------|
| Ops logging | `agent.SlogHooks` | event types, agent names, message counts/sizes | daily debugging, logs safe to ship |
| Full audit | `audit.NewSlog` | inputs / full messages / model outputs / tool args and results / guardrail verdicts / handoffs / usage — all **raw**, correlated by `run_id` | compliance, incident replay |
| Tracing | `tracing.Tracer` | span hierarchy (run → model.chat → tool.*) | OTel-style distributed tracing |

**Key points**:
- Audit contains raw content — user input and potential secrets — so it must go to a protected sink (a 0600 file, a dedicated log service), never public logs.
- Custom hooks implement `agent.Hooks`; `HandoffHook`/`GuardrailHook` are optional extension interfaces — not implementing them doesn't break composition.

## 10. MCP servers

**When you need it**: you want ready-made MCP tools (filesystem, browser, internal services …) without writing adapters.

```go
c, err := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportStdio, // launch the MCP server as a child process
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-everything"},
    Policy:    tool.Denylist("write_file"), // optional: authorize before each call
})
if err != nil {
    log.Fatal(err)
}
if err := c.Connect(ctx); err != nil { // spawn child + MCP handshake
    log.Fatal(err)
}
defer c.Close() // terminate the child process

tools, err := c.Tools(ctx) // []tool.Tool, schema passthrough and result flattening done
myAgent.Tools = append(myAgent.Tools, tools...)

res, err := agent.NewRunner().Run(ctx, myAgent, "Use a tool to compute 40 + 2")
```

Remote MCP services go over HTTP:

```go
c, err := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportHTTP,
    URL:       "https://mcp.example.com/mcp",
})
```

**Semantics**:
- Remote tool inputSchemas pass through to the model untouched; results are flattened to text (binaries become size placeholders); a server-side `IsError` maps to a tool error fed back to the model.
- `Config.Policy` authorizes **every** remote call (reuses `tool.Policy`): a denial sends nothing and reports the reason back to the model. For human approval, implement a custom `Policy` (show a confirmation in the callback).
- Authorization denials don't fail the run — they land in `res.ToolErrors`, and the model sees "denied, and why".

## 11. Resilience (retry, timeout, rate limit, fallback)

**When you need it**: production always has 429s, 5xx, timeouts and jitter.

Every model-side failure is a typed `*model.ModelError`; classify first, then decide:

```go
_, err := agent.NewRunner().Run(ctx, myAgent, "hi")
var me *model.ModelError
if errors.As(err, &me) {
    // me.Kind: ErrorRateLimited / ErrorServerError / ErrorTimeout / ErrorNetwork /
    //          ErrorAuth / ErrorInvalidRequest / ErrorProtocol
    // me.Retryable: whether this class is worth retrying
    // me.StatusCode / me.Provider / me.Body: details for diagnosis
}
```

The resilience middlewares are `model.Model` decorators that compose freely:

```go
primary := model.WithRateLimit(                       // rate limit: lazy token bucket, no background goroutines
    model.WithRetry(                                  // retry: exponential backoff + jitter, retryable classes only
        model.WithTimeout(openai.New(cfg), 60*time.Second), // per-call timeout
        model.DefaultRetryPolicy(),
    ),
    5, 5, // 5 requests per second, burst 5
)
backup := model.WithRetry(
    model.WithTimeout(openai.New(backupCfg), 60*time.Second),
    model.DefaultRetryPolicy(),
)
m := model.Fallback(primary, backup) // fallback chain: retryable failures switch to the backup

myAgent.Model = m
```

**Key points**:
- `context.Canceled` (the user cancelled) always passes through untouched — it is **never retried**.
- The fallback chain only switches on errors a different backend could fix (429/5xx/timeout/network); `invalid_request` / `protocol` errors fail fast — another backend would fail the same way.
- The Runner adds two more gates: `Runner.ToolTimeout` (per-tool timeout) and `Runner.MaxTurns` (loop budget).

## 12. Testing your agent

**When you need it**: test "when the model receives X, does the agent do Y" without tokens or real faults.

`testutil.Scripted` replays a scripted model and **records every request** for assertions:

```go
m := testutil.NewScripted(
    testutil.ToolCallStep("c1", "get_weather", `{"city":"Beijing"}`),
    testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
        // assert on what the model received (tool result fed back? system prompt right?)
        found := false
        for _, msg := range req.Messages {
            if msg.Role == model.RoleTool && strings.Contains(msg.Content, "sunny") {
                found = true
            }
        }
        if !found {
            return nil, errors.New("model never received the tool result")
        }
        return testutil.TextStep("done").Resp, nil
    }},
)

res, err := agent.NewRunner().Run(ctx, &agent.Agent{
    Model: m, Tools: []tool.Tool{weatherTool},
}, "Weather in Beijing?")
// res.Output == "done"

t.Logf("model received %d requests", m.Calls())
_ = m.LastRequest() // deep copy of the most recent request, safe to assert on
```

Streaming has a fake model too:

```go
sm := testutil.NewScriptedStream(testutil.StreamStep{
    Deltas: []model.StreamEvent{
        testutil.TextChunk("he"),
        testutil.TextChunk("llo"),
    },
    FinishReason: "stop",
})
// sm implements both model.Model and model.StreamModel
run := agent.NewRunner().RunStream(ctx, &agent.Agent{Model: sm}, "hi")
res, err := run.Wait()
```

**Key points**:
- Scripts replay in order and report `testutil.ErrScriptExhausted` when exhausted — which is itself an assertion ("there should not have been a third call").
- Fault injection: `Step.Err`, `testutil.HTTPErrorStep(429, "rate limited")`, combined with lesson 11's `WithRetry` to verify resilience.
- Sustained load and leak checks: `SOAK_ITERS=3000 make soak` (iterations configurable) over streaming / fault injection / all three session stores / compressors.

## 13. Built-in security tools (sandbox)

**When you need it**: the moment you want the agent to run commands or read files — the shell is the largest attack surface in production. Function tools (lesson 4) keep the execution point in your code but offer no isolation, and local MCP servers (lesson 10) run with your privileges too. Built-in security tools pull the execution point into the SDK: every command runs inside a **kernel-level sandbox** with writes limited to the workspace and networking disabled, with deny rules and approval as further layers.

Create a sandbox first (`sandbox.Auto` provides safe defaults: workspace writable, common system paths read-only, network denied, 30s default timeout):

```go
sb, err := sandbox.Auto("/path/to/workspace") // creates the directory
if err != nil { log.Fatal(err) }             // fail-closed by default: errors out on unsupported kernels
defer sb.Close()
```

Then attach the two built-in tools to an agent:

```go
shell, err := builtins.NewShellTool(builtins.ShellConfig{
    Workspace: "/path/to/workspace",
    Sandbox:   sb, // required: refuses to register an unconfined shell
})
reader, err := builtins.NewFileTool(builtins.FileConfig{
    Roots: []string{"/path/to/workspace"}, // read-only; escapes, symlinks, binaries and oversize are refused
})

a := &agent.Agent{
    Name:  "ops",
    Model: model, // any model.Model
    Tools: []tool.Tool{shell, reader},
}
```

The model can now call `shell` (argument `{"command":"..."}`) and `read_file` (argument `{"path":"..."}`). The tool arguments are themselves the commands and paths, so the audit layer from lesson 9 **records every executed command verbatim** with zero extra code.

Dangerous commands are stopped on two layers. The first is deny rules (on by default via `builtins.DefaultDenyRules()`: recursive deletes, mkfs, `curl | sh`, sudo, writes into `/etc`, ...). A hit returns `*tool.AuthorizationError`, whose text is fed back to the model as the tool result:

```go
_, err := shell.Run(ctx, `{"command":"rm -rf /data"}`)
// err: tool "shell" denied by policy: command matches deny rule "..."
```

The second layer is approval: wrap with `tool.WithPolicy` from lesson 4; a human-in-the-loop policy blocks synchronously until a person decides:

```go
approved := tool.WithPolicy(shell, tool.PolicyFunc(func(ctx context.Context, call tool.ToolCall) error {
    var args struct{ Command string `json:"command"` }
    json.Unmarshal([]byte(call.Arguments), &args)
    return askHuman(ctx, args.Command) // nil allows, error denies
}))
```

**Key points**:
- **Fail-closed all the way down**: `sandbox.Auto` errors out on kernels without Landlock (Linux ≥ 5.13; denying network needs ABI v4, kernel ≥ 6.7), and `NewShellTool` refuses to register without a sandbox. Only an explicit `Config.Lax=true` downgrades, and `sb.Capabilities()` then reports honestly which restrictions actually took effect.
- **The sandbox is the security boundary; deny rules are just a guardrail**: they catch obvious mistakes and are trivially bypassed (rename `rm`); the real defense is Landlock — writes outside the workspace and any networking are denied by the kernel, continuously verified by the escape test matrix (reading `/etc`, symlink escapes, dialing, timeout tree-kill) in CI.
- **It cannot stop prompt-injected but legitimate operations**: a model tricked into writing a malicious file inside the workspace is not stopped by the sandbox — that threat class is handled by `WithPolicy` approval plus the audit trail.
- **A `Sandbox` is long-lived** (on Linux each instance keeps one dedicated spawn thread): create one per agent/tool, not per call; `Close` kills any still-running process tree.
- Platform differences: Linux = Landlock + process-group kill + optional prlimit resource limits; Windows = Job Object tree kill (best effort, no path/network restrictions); `Capabilities()` reports which bits are real. For stronger isolation, run execution inside a container or remote sandbox (e.g. Firecracker) — the SDK-side design leaves room for that.

## Appendix A: Architecture overview

```
                ┌────────────────────────────────────────┐
                │            agent.Runner                │
                │  loop: call model → run tools → feed   │
                │  results back → call again             │
                │  hooks + spans + tool guards +         │
                │  sessions/compression                  │
                └───────┬──────────────────┬─────────────┘
                        │                  │
              model.Model            tool.Tool
                        │                  │
        ┌───────────────┼──────┐    ┌──────┼─────────┬──────────┐
        │ resilience    │      │    │ tool │ skill   │ handoff  │ mcp
        │ retry/timeout │ openai│   │ func │ SKILL.md│ transfer/│ stdio/
        │ ratelimit     │adapter│   │ tools│         │ as-tool  │ http
        │ fallback      └──────┘    └──────┴─────────┴──────────┘
        └───────────────┘
```

Dependencies are strictly one-way (no cycles): `tool → model`; `skill`/`mcp → tool`; `agent → model + tool + tracing`; `handoff`/`audit`/`guardrail`/`session → agent`; `testutil → model`.

Zero-dependency boundary: `model`/`tool`/`agent`/`session`/`tracing`/`testutil` use only the standard library; the two production dependencies are the official `modelcontextprotocol/go-sdk` (for `mcp`) and `modernc.org/sqlite` (pure Go, no CGO, for the session store).

## Appendix B: Differences from openai-agents-python

Three semantics are intentionally different when migrating from the Python version:

| Semantic | openai-agents-python | This SDK | Why |
|----------|---------------------|----------|-----|
| Input guardrail timing | runs **in parallel** with the first model call (saves a round trip) | **gate first, then call**: guardrails finish before the first request is sent | emitting tokens before reporting a trip is unacceptable for streaming safety; gating first guarantees "a trip means zero model calls" and streaming consistency |
| Session write-back | written back at the end of each turn | written back **on success only**; failed runs never persist | a half-finished conversation (guardrail trip, model error) must not pollute the next turn's context |
| History compression | — (not built in) | view-level compression: lossless storage, shrunken model view | `res.Messages` is exactly what the model saw; audit and debugging don't split apart |

The other core concepts (Agent/Runner/Handoff/guardrail tripwire/MaxTurns) align semantically; migration cost is mostly the type system: Python's decorators and runtime registration map to explicit struct fields and interface implementations.
