# agent-sdk（Go 版智能体开发 SDK）

**English** — agent-sdk is a production-oriented Go SDK for building AI agents, inspired by `openai-agents-python`. It provides an agent loop with function tools, MCP server integration (stdio & streamable-http) with per-tool authorization policies, first-class handoffs and nested agent-as-tool delegation, typed structured output, skills, a built-in error taxonomy, retry / timeout / rate-limit / fallback for model calls, lifecycle hooks & tracing, and incremental streaming (`Runner.RunStream` over SSE). Core packages depend only on the Go standard library; MCP support builds on the official `modelcontextprotocol/go-sdk`.

### Quick Start (English)

```go
import (
    "github.com/jiangfufa233/smart-agent-sdk-go/agent"
    "github.com/jiangfufa233/smart-agent-sdk-go/model/openai"
)

m := openai.New(openai.Config{
    APIKey:       os.Getenv("OPENAI_API_KEY"), // BaseURL 可指向 vLLM/Ollama/Qwen 等兼容端点
    DefaultModel: "gpt-4o-mini",
})
res, err := agent.NewRunner().Run(ctx, &agent.Agent{
    Instructions: "You are a helpful assistant.",
    Model:        m,
}, "Hello!")
fmt.Println(res.Output)
```

Streaming (events while the model generates):

```go
run := agent.NewRunner().RunStream(ctx, myAgent, "Hello!")
for ev := range run.Events {
    switch ev.Type {
    case agent.StreamTextDelta:
        fmt.Print(ev.Text)
    case agent.StreamFinalOutput:
        fmt.Printf("\ndone: %s (%d tokens)\n", ev.Text, ev.Usage.TotalTokens)
    case agent.StreamRunError:
        log.Printf("run failed: %v", ev.Err)
    }
}
res, err := run.Result()
```

MCP server tools (launched as a child process, or reached over HTTP):

```go
c, _ := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportStdio,
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-everything"},
    Policy:    tool.Denylist("write_file"), // optional authorization
})
_ = c.Connect(ctx)
defer c.Close()
mcpTools, _ := c.Tools(ctx)
agent.Tools = append(agent.Tools, mcpTools...)
```

First-class handoffs (the conversation continues with the target agent) and typed structured output:

```go
specialist := &agent.Agent{Instructions: "You are a research specialist.", Model: specialistModel}
myAgent.Handoffs = []agent.Handoff{handoff.New(specialist)}
res, _ := agent.NewRunner().Run(ctx, myAgent, "Find papers on X.")
// res.Transfers == []string{"specialist"}, res.Agent == specialist

type Report struct {
    City   string  `json:"city"`
    TempC  float64 `json:"temp_c"`
}
typed, err := agent.RunTyped[Report](ctx, agent.NewRunner(), myAgent, "weather in Beijing?")
fmt.Println(typed.Value.City) // decoded into the Go type
```

Runnable examples live in `examples/`; API reference: [pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go](https://pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go).

---

一个受 `openai-agents-python` 启发的生产级 Go 智能体 SDK，提供多轮对话、函数工具（Tools）、MCP 与 Skills 接入、智能体编排，以及内建的错误分类、重试/超时/限流/降级、结构化日志与追踪挂点。

- **核心零依赖**：`model`/`tool`/`agent` 仅使用 Go 标准库；`mcp` 包基于官方 `modelcontextprotocol/go-sdk`（本库唯一生产依赖）；测试使用 `go.uber.org/goleak`。
- **Go 版本要求**：`go 1.25`+（`go.mod`；由 go-sdk 依赖决定）。
- **模块路径**：`github.com/jiangfufa233/smart-agent-sdk-go`。

## 目录结构

```
agent-sdk/
├── go.mod                  # 模块定义
├── README.md               # 本文档
├── Makefile                # fmt/vet/test/race/cover/bench/check
├── .golangci.yml           # golangci-lint v2 配置
├── .github/workflows/ci.yml# CI：gofmt + vet + race 测试 + 覆盖率门禁 + lint
├── model/                  # 模型抽象层
│   ├── model.go            # 消息/请求/响应类型 + Model 接口（wire 兼容性表面）
│   ├── stream.go           # StreamModel/StreamReader 流式接口 + 工具调用增量聚合
│   ├── errors.go           # ModelError 错误分类学（Kind/Retryable/StatusCode）
│   ├── retry.go            # WithRetry：指数退避 + 抖动，按错误类别重试
│   ├── timeout.go          # WithTimeout：单次调用超时
│   ├── ratelimit.go        # WithRateLimit：惰性 token bucket（无后台 goroutine）
│   ├── fallback.go         # Fallback：多模型降级链
│   ├── sse/                # WHATWG 规范 SSE 增量解码器（含 fuzz 测试）
│   └── openai/
│       ├── openai.go       # OpenAI Chat Completions 适配器
│       ├── stream.go       # ChatStream：SSE chunk 解析 / tool call 增量 / usage
│       └── openai_test.go  # httptest 契约测试（wire 格式/状态码映射/取消语义/流式）
├── tool/                   # 工具运行时
│   ├── tool.go             # Tool 接口 + 反射版函数工具适配器
│   ├── schema.go           # 反射生成 JSON Schema
│   └── policy.go           # 工具权限/审批：Policy + WithPolicy 守卫
├── agent/                  # 智能体与执行循环
│   ├── agent.go            # Agent 配置类型（含 Handoffs 转交声明）
│   ├── runner.go           # Runner 工具调用循环（埋点/panic recover/超时/截断/转交）
│   ├── handoff.go          # 一等 Handoff：transfer_to_* 工具规格 + 注册表 + 系统消息切换
│   ├── structured.go       # RunTyped[T]：schema 注入 + 结构化输出解码
│   ├── stream.go           # Runner.RunStream 事件流（流式 + 非流式自动降级）
│   ├── errors.go           # MaxTurnsError / ToolError
│   └── hooks.go            # 生命周期 Hooks 接口 + slog 实现（含可选 HandoffHook）
├── tracing/
│   └── tracing.go          # Tracer/Span 最小接口 + Nop + slog 实现
├── handoff/
│   └── handoff.go          # 智能体间委托：一等 Handoff 构造器 + agent-as-tool
├── skill/
│   └── skill.go            # SKILL.md 技能加载器
├── mcp/
│   ├── mcp.go              # MCP 客户端：stdio / streamable-http + 握手 + 生命周期
│   └── adapter.go          # 远端 MCP 工具 → tool.Tool 适配（schema/结果展平/IsError）
├── testutil/
│   ├── testutil.go         # 脚本化假模型（测试基建，亦可用于用户测试）
│   └── stream.go           # 脚本化流式假模型（StreamStep/TextChunk/ToolCallChunk）
└── examples/               # 可运行示例
    ├── chat/main.go        # 多轮终端对话
    ├── tools/main.go       # 函数工具调用
    ├── mcp/                # MCP：stdio 子进程 + 策略拒绝 + agent 循环
    │   ├── main.go
    │   └── server/main.go
    └── offline/main.go     # 无网络冒烟测试（基于 testutil）
```

## 架构总览

```
                ┌────────────────────────────────────────┐
                │            agent.Runner                │
                │  循环：调模型 → 执行工具 → 回填结果 → 再调模型  │
                │   hooks + tracing spans + 工具防护        │
                └───────┬──────────────────┬─────────────┘
                        │                  │
              model.Model 接口        tool.Tool 接口
                        │                  │
        ┌───────────────┼──────┐    ┌──────┼─────────┬──────────┐
        │  韧性中间件     │      │    │ tool │ skill   │ handoff  │ mcp
        │ retry/timeout │ openai│   │ 函数  │ SKILL.md│ 一等转交/ │ stdio/
        │ ratelimit     │ 适配器 │   │ 工具  │ 渐进披露 │ as-tool  │ http
        │ fallback      └──────┘    └──────┴─────────┴──────────┘
        └───────────────┘
```

依赖方向严格单向，避免循环依赖：

- `tool` → `model`（工具规格使用 `model.ToolParam`）
- `tracing` → 无依赖
- `skill` → `tool`；`mcp` → `tool` + 官方 go-sdk
- `agent` → `model` + `tool` + `tracing`
- `handoff` → `agent` + `tool`
- `testutil` → `model`
- `examples/*` → 以上全部

### 核心执行流程（Runner 循环）

1. 组装消息：`system`（Agent 指令）+ 历史消息 + 本轮 `user` 输入；
2. 携带所有工具定义调用 `Model.Chat`（触发 `OnLLMCall/OnLLMResponse` hooks 与 `model.chat` span）；
3. 若模型返回 `tool_calls`：逐个执行对应 `tool.Tool.Run`——带独立超时、panic recover、输出截断（默认 512 KiB），失败信息以文本回填并记入 `RunResult.ToolErrors`；
4. 若 `tool_calls` 中含转交工具（`Agent.Handoffs` 声明的 `transfer_to_*`）：记入转交标记结果，待该消息所有工具调用都有结果后切换"当前智能体"——重建工具面与采样设置、以目标智能体的指令替换 system 消息，继续同一对话（`RunResult.Agent`/`Transfers` 报告转交路径；`MaxTurns` 约束所有智能体的模型调用总数）；
5. 若模型直接返回文本：作为最终答案结束，`RunResult` 附带 `Usage`、`Duration`、`RunID`；
6. 超过 `Runner.MaxTurns`（默认 10）返回 `*MaxTurnsError`（`errors.Is(err, ErrMaxTurns)` 兼容）。

## 可靠性设计（生产语义）

| 机制 | 说明 |
|------|------|
| 错误分类学 | 所有 provider 失败统一为 `*model.ModelError{Kind, Retryable, StatusCode, Provider, Body}`；`errors.As` 即可判定。`context.Canceled` 永远原样透传，不会被误重试。 |
| 重试 | `model.WithRetry`：指数退避 + 抖动 + 上限，默认只重试 `Retryable` 错误（429/5xx/超时/网络），401/400 类不重试。 |
| 超时 | `model.WithTimeout` 单次调用超时；`Runner.ToolTimeout` 工具执行超时。 |
| 限流 | `model.WithRateLimit` 惰性 token bucket，无后台 goroutine，ctx 感知。 |
| 降级链 | `model.Fallback(primary, secondary...)`：可重试类失败自动切换候选；invalid_request/protocol 类停止（换后端也解决不了）。 |
| 工具防护 | panic recover、独立超时、输出截断（防止撑爆模型上下文）；失败同时记入 `RunResult.ToolErrors` 供审计。 |
| 工具权限 | `tool.Policy` + `tool.WithPolicy`：Allowlist/Denylist/自定义审批回调（可阻塞等人工确认），拒绝以类型化 `*tool.AuthorizationError` 报告且不执行工具，拒绝原因回填给模型；MCP 远端工具经 `mcp.Config.Policy` 一行接入。 |
| 智能体编排 | 一等 Handoff（`agent.Handoff`/`handoff.New`）：每个转交暴露为空参 `transfer_to_<name>` 工具，调用后同一对话继续由目标智能体处理（工具面/设置/系统提示切换，全历史共享）；校验 nil 目标、nil 目标模型与工具名冲突；`RunResult.Agent`/`Transfers` + `StreamHandoff` 事件 + 可选 `agent.HandoffHook` 全程可观测；嵌套 agent-as-tool（`handoff.AsTool`）保留用于子任务隔离。 |
| 结构化输出 | `agent.RunTyped[T]`：由 Go 类型反射生成 `json_schema` 响应格式注入请求（不修改原 Agent、已有 `ResponseFormat` 则尊重不改），最终输出剥 Markdown 围栏后解码进 `TypedResult.Value`；解码失败返回 `*agent.StructuredOutputError`（含原始文本），`errors.As` 可判。 |
| 流式 | `Runner.RunStream` 事件流：文本/工具调用增量、`StreamFinalOutput`/`StreamRunError` 终态事件恰好一个；模型未实现流式自动降级为单次 `Chat`。SSE 层（`model/sse`）符合 WHATWG 规范（LF/CRLF/CR、注释 keepalive、BOM），带 fuzz 测试；OpenAI 流式适配器把"无 finish_reason 也无 [DONE] 就断流"明确报为 protocol 错误，不会静默当成功。`context.Canceled` 原样透传。 |
| 泄漏防护 | 框架不启动后台 goroutine（事件流的生产 goroutine 随 ctx 取消或事件耗尽退出）；测试以 goleak 验证。 |
| 可观测 | `Runner.Hooks`（生命周期事件，`agent.SlogHooks` 一行接入结构化日志，只记标识与大小、不记原文与密钥）；`tracing.Tracer/Span` 最小接口，Nop 默认，OTel 适配器无需 SDK 依赖即可实现。 |

## 包与文件说明

### `model/` —— 模型抽象层

| 文件 | 作用 |
|------|------|
| `model.go` | 与厂商无关的 wire 类型：`Message`（多模态：`Content` 字符串或 `Parts` 内容数组，未知 part 类型经 `Extra` 原样透传）、`ToolCall`、`ToolParam`、`Usage`（含 cached tokens 与 `Accumulate`）、`Settings`（temperature/top_p/stop/seed/tool_choice/response_format 等，扁平序列化）、`Request`、`Response`；接口 `Model` 与适配器 `ModelFunc`。**此为冻结的兼容性表面。** |
| `stream.go` | 可选流式接口：`StreamModel{ChatStream}`、`StreamReader{Next/Event/Err/Close}`（sql.Rows 风格）、`StreamEvent{text/tool_call/finish}`、`AsStream` 非流式降级适配、`ToolCallAccumulator` 按 Index 聚合增量。 |
| `errors.go` | `ModelError` 错误分类学；`ClassifyHTTPStatus`/`NewHTTPError`/`ClassifyTransportError`。 |
| `retry.go` / `timeout.go` / `ratelimit.go` / `fallback.go` | 全部为 `Model` 装饰器，可自由组合，如：`model.Fallback(model.WithRetry(model.WithTimeout(openai.New(cfg), 30*time.Second), p1), backup)`。 |
| `sse/` | WHATWG 规范的增量式 SSE 解码器：任意分块输入事件一致（fuzz 验证）、注释 keepalive、行长度上限防恶意服务器、EOF 未完事件仍派发。 |
| `openai/openai.go` | Chat Completions 标准库实现。`BaseURL` 可指向任意 OpenAI 兼容端点（vLLM、Ollama、Qwen 等）。所有失败返回 `*model.ModelError`。 |
| `openai/stream.go` | 流式实现：`stream:true` + `include_usage`（`Config.DisableStreamUsage` 可关闭）、chunk 解析、tool call 增量转发、`StreamFinish` 收尾（含 finish_reason/usage）、断流语义见上表。 |

### `tool/` —— 工具运行时

`tool.go` 定义 `Tool` 接口与 `NewFunction` 反射适配器（支持 `func(in Args) (T, error)` 及前置 `ctx` 变体）；`schema.go` 递归生成 JSON Schema（`json`/`desc` 标签、required 推导、`time.Time` 映射）；`policy.go` 提供调用前授权：`Policy` 接口（`PolicyFunc`/`Allowlist`/`Denylist`/`AllowAll`）+ `WithPolicy` 守卫装饰器，拒绝返回 `*AuthorizationError`（`errors.As` 可判），工具不执行、原因回填模型。

### `agent/` —— 智能体与执行循环

`agent.go`（`Agent{Name, Instructions, Model, ModelName, Tools, Handoffs, Settings}`）、`runner.go`（核心循环 + 生产语义 + 转交语义）、`handoff.go`（`Handoff{Target, Name, Description}` 转交声明：`Spec()` 生成 `transfer_to_<slug>` 空参工具、`buildAgentRegs` 注册表与冲突校验、`switchSystemMessage` 系统提示切换）、`structured.go`（`RunTyped[T]`/`TypedResult[T]`/`StructuredOutputError` + 围栏剥离）、`stream.go`（`Runner.RunStream/RunStreamWithHistory` 事件流：`StreamRunStarted/TextDelta/ToolCallStarted/Args/Finished/ToolResult/Handoff/FinalOutput/RunError`，非流式模型自动降级，`Wait()` 一步收取结果）、`errors.go`（`MaxTurnsError`/`ToolError`）、`hooks.go`（`Hooks` 接口、可选扩展 `HandoffHook`、`NopHooks`、`SlogHooks`）。

### `tracing/` —— 追踪挂点

`Tracer{Start(ctx, name) (ctx, Span)}` + `Span{Set, End(err)}`；`Nop()` 默认、`NewSlog(*slog.Logger)` 输出 span 日志；`SpanFromContext` 取当前 span。Runner 为 run、每次模型调用、每次工具调用建 span。

### `testutil/` —— 测试基建

`Scripted` 假模型：按序回放步骤、录制每次请求（深拷贝，防调用方修改污染断言）、支持错误注入/延迟/`Func` 动态响应；`TextStep`/`ToolCallStep`/`HTTPErrorStep` 构造器。`ScriptedStream` 流式假模型：`StreamStep` 定义增量序列、finish/usage、请求级与流中错误注入、逐 delta 延迟（测取消与慢消费者）；同一份脚本可分别走 `Chat` 与 `ChatStream` 验证一致性。SDK 自身测试与 `examples/offline` 均基于它，推荐用户用它给自己的智能体写测试。

### `mcp/` —— MCP 接入（基于官方 go-sdk）

`mcp.go`（`Client`：`NewClient(Config)` → `Connect` 握手 → `Tools` 列出远端工具 → `Close`；`TransportStdio` 以子进程启动 server，`Close` 终止；`TransportHTTP` 走 streamable-http，`DisableStandaloneSSE` 可关）；`adapter.go`（远端工具适配为 `tool.Tool`：inputSchema 透传、结果展平为文本——文本直传/二进制与 blob 以尺寸占位/结构化输出去重附加、`IsError` 映射为错误并回填模型；空参数映射为 `{}`）。`Config.Policy` 对每次远端调用做授权（见 `tool.Policy`）。测试覆盖内存传输全栈、真子进程 stdio 端到端、httptest HTTP 端到端、进程崩溃/连接拒绝故障注入，goleak 把关。

## 扩展指南

**接入新的模型提供商**：实现 `model.Model` 接口（一个 `Chat` 方法）；失败请返回 `*model.ModelError`（用 `ClassifyHTTPStatus`/`ClassifyTransportError` 构造）以获得正确的重试与降级行为。OpenAI 兼容协议直接用 `openai.Config{BaseURL: ...}`。

**新增工具**：

```go
type searchArgs struct {
    Query string `json:"query" desc:"搜索关键词"`
}
t, err := tool.NewFunction("search", "搜索内部知识库",
    func(ctx context.Context, in searchArgs) (string, error) { ... })
agent.Tools = append(agent.Tools, t)
```

**给智能体写测试**：

```go
m := testutil.NewScripted(
    testutil.ToolCallStep("c1", "search", `{"query":"go"}`),
    testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
        // 断言模型收到的工具结果，再返回最终答案
        return testutil.TextStep("done").Resp, nil
    }},
)
res, err := agent.NewRunner().Run(ctx, &agent.Agent{Model: m, Tools: []tool.Tool{t}}, "q")
```

**接入 MCP server**：

```go
c, err := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportStdio,
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-everything"},
    Policy:    tool.Denylist("write_file"), // 可选：调用前授权
})
if err != nil { ... }
if err := c.Connect(ctx); err != nil { ... }  // 启动子进程 + MCP 握手
defer c.Close()
tools, err := c.Tools(ctx)                    // []tool.Tool，直接挂给 agent
agent.Tools = append(agent.Tools, tools...)
```

streamable-http 用 `mcp.Config{Transport: mcp.TransportHTTP, URL: "https://..."}`；自定义审批（人工确认）实现 `tool.Policy` 传入即可。

**智能体编排（Handoff 与 agent-as-tool）**：

```go
// 一等转交：目标智能体在同一对话中接管（共享全历史与 token 预算）。
specialist := &agent.Agent{Name: "specialist", Instructions: "...", Model: m2}
myAgent.Handoffs = []agent.Handoff{handoff.New(specialist)}
res, err := agent.NewRunner().Run(ctx, myAgent, "研究一下 X")
// res.Transfers == []string{"specialist"}，res.Agent == specialist

// 嵌套委托（agent-as-tool）：子任务独立成 run，只把最终答案交回上级。
subTool, err := handoff.AsTool(specialist, nil)
agent.Tools = append(agent.Tools, subTool)
```

转交链可观测：`agent.StreamHandoff` 事件携带 `FromAgent/ToAgent`；实现 `agent.HandoffHook`（可选扩展 `agent.Hooks`）即可在 `OnHandoff` 中收到每次转交。

**结构化输出（typed JSON）**：

```go
type Report struct {
    City   string  `json:"city"`
    TempC  float64 `json:"temp_c" desc:"摄氏温度"`
}
typed, err := agent.RunTyped[Report](ctx, agent.NewRunner(), myAgent, "北京天气？")
// typed.Value.City；失败时 errors.As(err, *agent.StructuredOutputError) 拿原始文本
```

`RunTyped` 反射 `Report` 生成 `json_schema` 响应格式注入请求（不改原 Agent，已有 `ResponseFormat` 则尊重现值），并容忍模型在 JSON 外包一层 Markdown 代码围栏。

**接入结构化日志与追踪**：

```go
r := &agent.Runner{
    Hooks:   agent.SlogHooks(slog.Default()),
    Tracer:  tracing.NewSlog(slog.Default()),
}
```

**生产级模型客户端推荐组合**：

```go
primary := model.WithRateLimit(model.WithRetry(model.WithTimeout(openai.New(cfg), 60*time.Second), model.DefaultRetryPolicy()), 5, 5)
backup  := model.WithRetry(model.WithTimeout(openai.New(backupCfg), 60*time.Second), model.DefaultRetryPolicy())
m := model.Fallback(primary, backup)
```

**流式输出**：模型实现（或经 `model.AsStream` 降级包装）即可用 `Runner.RunStream` 逐事件消费；只要 `Wait()` 拿最终结果、或 range 到 channel 关闭，二选一：

```go
run := agent.NewRunner().RunStream(ctx, a, "讲个故事")
for ev := range run.Events {   // 一直消费到关闭，或改用 run.Wait()
    switch ev.Type {
    case agent.StreamTextDelta:        // 逐 token 文本
        fmt.Print(ev.Text)
    case agent.StreamToolCallArgs:     // 工具参数增量（ev.Call）
    case agent.StreamToolResult:       // 工具结果（ev.Result / ev.ToolErr）
    case agent.StreamFinalOutput:      // 终态：全文 + finish_reason + 累计 usage
    case agent.StreamRunError:         // 终态：失败（*model.ModelError / MaxTurnsError / ctx 错误）
    }
}
res, err := run.Result()
```

自定义 provider 想支持流式：额外实现 `model.StreamModel`（`Chat` 仍必须实现，供降级），事件语义遵循 `model.StreamReader` 文档；不实现则自动走非流式，无需额外工作。

## 当前状态与路线图

| 能力 | 状态 |
|------|------|
| 多轮对话 / Runner 循环 + hooks + tracing | ✅ 可用 |
| 函数工具 + 反射 JSON Schema | ✅ 可用 |
| 错误分类学 / 重试 / 超时 / 限流 / 降级链 | ✅ 可用（P0） |
| 流式输出（SSE 解析 + StreamModel + Runner.RunStream 事件流 + 非流式降级） | ✅ 可用（P1） |
| 测试基建（testutil 假模型 + 契约测试 + CI 门禁） | ✅ 可用（P0） |
| Skills（SKILL.md 加载 + 渐进披露） | ✅ 基本可用 |
| 智能体编排（一等 Handoff 转交语义 + agent-as-tool） | ✅ 可用（P3） |
| 结构化输出（RunTyped[T] schema 注入 + 类型化解码） | ✅ 可用（P3） |
| MCP 客户端（stdio / streamable-http + 工具适配）+ 工具权限/审批 | ✅ 可用（P2） |
| Guardrails + 全量审计日志 | ⬜ P4，下一阶段 |
| 会话持久化 + 历史压缩 | ⬜ P5 |
| soak 测试 / 故障注入 / 发布流程 | ⬜ P6 |

**P0 完成标准（已达成）**：`go vet` / `go test -race` 全绿；goleak 无泄漏；库包覆盖率 86%（门禁 70%）；openai 适配器契约测试覆盖 wire 格式与状态码映射；离线冒烟全流程通过。

**P1 完成标准（已达成）**：SSE 解析器 fuzz（410 万次执行）无失败且分块输入与整流输入事件一致；OpenAI 流式契约测试覆盖文本/工具增量聚合、[DONE]、断流、429、坏 JSON、ctx 取消；Runner 事件流测试覆盖降级、多轮工具、错误注入、取消无泄漏（goleak）；离线冒烟含流式演示通过。

**P2 完成标准（已达成）**：MCP 客户端覆盖 stdio 与 streamable-http（官方 go-sdk v1.7）；内存传输全栈契约测试 + 真子进程 stdio 端到端 + httptest HTTP 端到端；故障注入覆盖命令不存在/进程秒退/死端点/Close 后调用/ctx 超时；`tool.Policy` 权限层单测覆盖 allow/deny/透传/防双重包裹/拒绝不执行；goleak 把关（含 go-sdk 服务端连接）；examples/mcp 演示策略拒绝 + 远程调用进入 agent 循环。

**P3 完成标准（已达成）**：一等转交在 `Run` 与 `RunStream` 两条循环语义一致（系统提示/工具面/设置切换、全历史共享、转交标记回填）；转交链（A→B→C）与并行工具调用+转交混合消息均覆盖；`MaxTurns` 约束跨智能体的模型调用总数（测试断言）；转交校验（nil 目标/nil 目标模型/工具名冲突/重复转交/切换目标非法）全部报错；`HandoffHook` 为可选扩展，普通 `Hooks` 实现者零改动兼容；`StreamHandoff` 事件含 `FromAgent/ToAgent` 且先于 `StreamFinalOutput`；`RunTyped` 覆盖 schema 注入、已有格式保留、设置保留且原 Agent 不被修改、围栏剥离、解码失败类型化错误、不支持类型报错；库包覆盖率 89%（门禁 70%）；goleak 全绿。

## 验证

```bash
make check          # vet + test + race + build（CI 同款门禁）
make cover          # 覆盖率明细（库包合计 89%）
make bench          # 基准（schema 生成、Runner 循环）
go run ./examples/offline   # 无网络端到端冒烟测试
go run ./examples/mcp       # MCP 端到端：子进程 server + 策略 + agent 循环
```

## License

[MIT](LICENSE)
