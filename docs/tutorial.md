# smart-agent-sdk-go 教程

从零开始，把一个 Go 智能体从"能对话"做到"能上生产"。每课回答三个问题：**什么时候需要它、代码怎么写、要注意什么**。

> English version: [tutorial.en.md](tutorial.en.md)（结构与代码块完全一致）
>
> API 细节参考：[pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go](https://pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go)

| 课程 | 内容 | 什么时候需要 |
|------|------|--------------|
| [0](#0-准备工作) | 环境与模型端点准备 | 开始之前 |
| [1](#1-第一个智能体) | 第一个智能体 | 跑通第一条对话 |
| [2](#2-多轮会话session) | 多轮会话 | 聊天机器人要记住上文 |
| [3](#3-长对话压缩) | 长对话压缩 | 对话太长撑爆上下文 |
| [4](#4-函数工具) | 函数工具 | 让模型查数据库、调 API |
| [5](#5-流式输出) | 流式输出 | 打字机效果、边生成边展示 |
| [6](#6-结构化输出) | 结构化输出 | 让模型返回 Go 结构体而不是文本 |
| [7](#7-智能体编排handoff) | 智能体编排 | 多个智能体协作/转交 |
| [8](#8-护栏guardrails) | 护栏 | 拦截敏感输入、审核输出 |
| [9](#9-可观测hooks审计追踪) | 可观测 | 日志、审计、追踪 |
| [10](#10-接入-mcp-服务器) | 接入 MCP | 复用现成的 MCP 工具生态 |
| [11](#11-韧性重试超时限流降级) | 韧性 | 生产环境的网络与配额问题 |
| [12](#12-测试你的智能体) | 测试 | 不花 token 地测智能体逻辑 |
| [13](#13-内置安全工具sandbox) | 内置安全工具 | 让智能体执行命令/读文件而不裸奔 |
| [附录 A](#附录-a架构总览) | 架构总览 | 想了解内部结构 |
| [附录 B](#附录-b与-openai-agents-python-的语义差异) | 与 openai-agents-python 的差异 | 从 Python 版迁移过来 |

## 0. 准备工作

**环境**：Go 1.25+。

```bash
go get github.com/jiangfufa233/smart-agent-sdk-go
```

**模型端点**：SDK 通过 OpenAI Chat Completions 协议调用模型，任何兼容端点都能用——OpenAI 官方、vLLM、Ollama、Qwen 服务等：

```go
// OpenAI 官方
m := openai.New(openai.Config{
    APIKey:       os.Getenv("OPENAI_API_KEY"),
    DefaultModel: "gpt-4o-mini",
})

// 本地 / 私有端点（Ollama 为例）
m := openai.New(openai.Config{
    APIKey:       "ollama", // 多数本地端点不校验，占位即可
    BaseURL:      "http://localhost:11434/v1",
    DefaultModel: "qwen2.5:7b",
})
```

**怎么跑课程代码**：第 1 课给出完整的 `main.go`，之后每课只给关键片段——把片段放进第 1 课的骨架（`main` 函数内）即可运行。涉及的标准库 import 按需补齐；SDK 包统一来自 `github.com/jiangfufa233/smart-agent-sdk-go/...`。

**可运行的完整示例**在仓库 `examples/` 目录：`chat`（多轮对话）、`tools`（工具调用）、`mcp`（MCP 接入）、`offline`（离线冒烟，覆盖全部特性，无网络可跑）。

## 1. 第一个智能体

一个智能体 = 一份系统指令（`Instructions`）+ 一个模型（`Model`）；一次运行 = `Runner.Run`。这是完整的、可跑的程序：

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
        Instructions: "你是一个简洁的中文助手。",
        Model:        m,
    }

    res, err := agent.NewRunner().Run(ctx, myAgent, "用一句话解释什么是智能体。")
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

示例输出（取决于模型）：

```text
智能体是能自主感知环境、使用工具并采取行动来完成目标的程序。
```

**`RunResult` 里有什么**：

| 字段 | 含义 |
|------|------|
| `Output` | 最终文本回答 |
| `FinalMessage` | 结束本次 run 的那条模型消息 |
| `Messages` | 完整对话（含 system、工具调用与结果），可传给 `RunWithHistory` 续聊 |
| `ToolErrors` | 本次 run 中失败的工具调用（错误文本已回填给模型，见第 4 课） |
| `Usage` | 跨轮累计的 token 用量 |
| `Duration` / `RunID` | 耗时与本次 run 的关联 ID（日志、审计用） |
| `Agent` / `Transfers` | 产出最终回答的智能体与转交路径（见第 7 课） |

**要点**：
- `Runner` 可以复用——它无状态，多个智能体共享一个即可。
- 每次模型调用消耗 1 个 turn，`Runner.MaxTurns` 默认 10，防止模型无限循环调工具。
- 不要忽略 `err`：所有模型侧失败都是类型化的 `*model.ModelError`（第 11 课），护栏/会话错误也各有类型。

## 2. 多轮会话（Session）

**什么时候需要**：聊天机器人要记住上一轮说了什么。

不用的写法是手工搬运历史：

```go
res1, err := agent.NewRunner().Run(ctx, myAgent, "我叫小江，最喜欢 Go。")
res2, err := agent.NewRunner().RunWithHistory(ctx, myAgent, res1.Messages, "我叫什么？")
```

`RunWithSession` 把装载与回写自动化：

```go
sess := session.NewInMemory()
r := agent.NewRunner()

_, err := r.RunWithSession(ctx, myAgent, sess, "我叫小江，最喜欢 Go。")
res, err := r.RunWithSession(ctx, myAgent, sess, "我叫什么？最喜欢什么？")
fmt.Println(res.Output) // 模型看得到上一轮
```

**三种存储**，接口相同（`GetItems` / `AddItems` / `Clear`）：

```go
sess := session.NewInMemory()               // 进程内，重启即失
sess = session.NewFile("chat.jsonl")        // JSONL 追加式，重启后可续聊
store, err := session.NewSQLiteStore("chats.db") // 单个 DB 文件承载多个会话
sess = store.Get("user-42")                 // keyed 视图，不同 id 互不可见
```

**语义要点**（值得花 30 秒读一遍，都是排过坑的）：
- run **成功后**才把本轮新消息（user 输入 + 生成的 assistant/tool 消息）回写进 session；**失败的 run 不落盘**——不会把半截对话留给下一轮。
- 回写失败会让 run 返回错误：静默丢上下文比显式失败更危险。
- system 提示词不入 session——它是 `Agent` 配置，每次 run 重新前置。换 `Instructions` 不用清 session。
- session 存的是**全量无损**历史；发给模型的视图可以压缩（第 3 课）。
- 转交产生的对话记录原样入 session（转交后的会话可以继续聊，见第 7 课）。

**常见坑**：`NewSQLiteStore` 的 DB 文件可以被多个进程共享（内置 busy_timeout 与 WAL），但它是单文件语义，不要当网络数据库用。

## 3. 长对话压缩

**什么时候需要**：会话跑了几百轮，历史超过模型上下文窗口。

给 `Runner` 配一个压缩器（`Compressor`）。压缩是**视图级**的：session 存储保持无损，只收缩发给模型的历史，`res.Messages` 反映的就是模型实际看到的视图。

```go
// 方式一：滑动窗口——保留 system + 最近 N 条，零成本
r := &agent.Runner{Compressor: session.NewSlidingWindow(40)}

// 方式二：滚动摘要——把老消息折叠成一条摘要（用便宜的小模型）
sum := session.NewSummarizer(cheapModel) // cheapModel 可以是另一个 openai 实例
sum.High, sum.Low = 50, 20               // 非系统消息超过 50 折叠，保留最近 20 条原文
r := &agent.Runner{Compressor: sum}
```

`Summarizer` 的省钱设计：
- **迟滞**：压缩后视图回落到 `Low`，涨回 `High` 才再压缩，不会每轮都调摘要模型；
- **增量折叠**：涨回去之后只把**新增的消息**折进已有摘要，不重摘全量；
- 摘要模型的调用次数 ≈ `(len−High)/(High−Low)`，而不是一轮一次。

**要点**：
- 摘要缓存绑定在 `Summarizer` 实例上：**复用同一个 Runner 就是复用缓存**；新建 Runner 会从头重新摘要。
- `High−Low` 差值越大，摘要调用越省，但每次折叠的内容越多、摘要越粗。50/20 是稳妥起点。
- 压缩只改视图，audit（第 9 课）记录的是压缩后的请求视图，session 里永远是全量原文。

## 4. 函数工具

**什么时候需要**：让模型去查数据库、调内部 API——SDK 负责整条"模型请求工具 → 执行 → 结果回填 → 模型继续"的循环。

写一个普通 Go 函数，参数结构体就是 JSON Schema：

```go
type weatherArgs struct {
    City string `json:"city" desc:"城市名，如 Beijing"`
    Days int    `json:"days" desc:"预报天数"`
}

weatherTool, err := tool.NewFunction("get_weather", "查询城市天气预报",
    func(ctx context.Context, in weatherArgs) (string, error) {
        return fmt.Sprintf("%s：晴，最高 21°C（%d 天）", in.City, in.Days), nil
    })
if err != nil {
    log.Fatal(err)
}

myAgent.Tools = []tool.Tool{weatherTool}
res, err := agent.NewRunner().Run(ctx, myAgent, "北京未来 3 天天气怎么样？")
fmt.Println(res.Output) // 模型已调用 get_weather 并基于结果作答
```

SDK 会反射 `weatherArgs` 生成 JSON Schema（`json` tag 定字段名，`desc` tag 定参数描述，required 自动推导）发给模型。函数签名支持 `func(in T) (string, error)` 或带前置 `ctx` 的变体。

**工具失败不炸 run**：工具返回 error（或 panic）时，错误文本会作为工具结果回填给模型，让它自行调整（换参数重试、换说法、向用户道歉），同时记进 `res.ToolErrors` 供审计：

```go
for _, te := range res.ToolErrors {
    log.Printf("工具 %s 参数 %s 失败: %v", te.Tool, te.Arguments, te.Err)
}
```

**要点**：
- 工具的 name 与 description 是模型决定"何时调用"的唯一依据，值得认真写。
- 工具返回值会占上下文，超长返回会被截断（`Runner.MaxToolOutputBytes`，默认 512 KiB）。
- `Runner.ToolTimeout` 可以给每次工具执行加独立超时。

## 5. 流式输出

**什么时候需要**：打字机效果；工具调用与转交过程实时可见。

模型客户端实现了流式接口（`openai.New` 的返回值天然支持），`RunStream` 返回事件流：

```go
run := agent.NewRunner().RunStream(ctx, myAgent, "讲个 100 字的小故事")
for ev := range run.Events {
    switch ev.Type {
    case agent.StreamTextDelta: // 逐段文本
        fmt.Print(ev.Text)
    case agent.StreamToolCallStarted, agent.StreamToolCallArgs, agent.StreamToolResult:
        // 工具调用的实时过程（ev.Call / ev.Result / ev.ToolErr）
    case agent.StreamHandoff:
        fmt.Printf("\n[转交 %s -> %s]\n", ev.FromAgent, ev.ToAgent)
    case agent.StreamFinalOutput: // 终态：全文 + 用量
        fmt.Printf("\n[完成，%d tokens]\n", ev.Usage.TotalTokens)
    case agent.StreamRunError: // 终态：失败
        log.Printf("run 失败: %v", ev.Err)
    }
}
res, err := run.Result()
```

事件类型全集：`StreamRunStarted` / `StreamTextDelta` / `StreamToolCallStarted` / `StreamToolCallArgs` / `StreamToolCallFinished` / `StreamToolResult` / `StreamHandoff` / `StreamFinalOutput` / `StreamRunError`。

**要点**：
- **终态事件恰好一个**：`StreamFinalOutput` 或 `StreamRunError`，随后 channel 关闭。两种正确消费姿势：range 到关闭，或 `run.Wait()`（自动排干并返回结果）。
- 只在结尾要结果、不在乎过程？`res, err := agent.NewRunner().RunStream(ctx, a, in).Wait()` 一行搞定。
- 模型没实现流式接口时自动降级为普通调用（全文作为单个 TextDelta），上层代码不用改。
- 与会话组合：`RunStreamWithSession(ctx, a, sess, input)`，回写发生在终态事件之前，单终态语义不变。

**常见坑**：发起 run 后既不消费 `Events` 也不 `Wait()`，事件缓冲填满后 run 会停住（背压设计）——不用的事件流要排干或取消 ctx。

## 6. 结构化输出

**什么时候需要**：下游代码要的是数据（表单、工单、抽取结果），不是一段话。

```go
type Report struct {
    City   string  `json:"city" desc:"城市名"`
    TempC  float64 `json:"temp_c" desc:"摄氏温度"`
    Sunny  bool    `json:"sunny" desc:"是否晴天"`
}

typed, err := agent.RunTyped[Report](ctx, agent.NewRunner(), myAgent, "北京现在的天气？")
if err != nil {
    var se *agent.StructuredOutputError
    if errors.As(err, &se) {
        log.Fatalf("模型没有输出合法 JSON，原文: %s", se.Raw)
    }
    log.Fatal(err)
}
fmt.Printf("%s %.1f°C 晴=%v\n", typed.Value.City, typed.Value.TempC, typed.Value.Sunny)
```

`RunTyped[Report]` 反射 Go 类型生成 JSON Schema 注入请求（不改原 `Agent`；若已配置 `ResponseFormat` 则尊重现值），最终输出解码进 `typed.Value`；`typed.Result` 是底层 run 结果（用量、转交路径等照常可用）。模型把 JSON 包在 Markdown 代码围栏里也能容忍。

**要点**：
- 字段上的 `desc` 标签会进入 schema，直接影响抽取质量，该写就写。
- 解码失败是类型化的 `*agent.StructuredOutputError`（携带原文），`errors.As` 拿到后可以重试或降级到人工处理。

## 7. 智能体编排（Handoff）

**什么时候需要**：一个智能体接不住所有事——分诊模型把研究类问题转给研究专员、客服把退款转给售后。

**一等转交（Handoff）**：每个转交暴露为一个 `transfer_to_<name>` 空参工具，模型调用后**同一个对话**由目标智能体继续——工具面、采样设置、系统提示全部切换，历史完整共享：

```go
researcher := &agent.Agent{
    Name:         "researcher",
    Instructions: "你是研究专员，负责深入检索并给出结构化结论。",
    Model:        m,
}
triage := &agent.Agent{
    Name:         "triage",
    Instructions: "你是分诊台：简单问题直接答；需要调研的转交 researcher。",
    Model:        m,
    Handoffs:     []agent.Handoff{handoff.New(researcher)},
}

res, err := agent.NewRunner().Run(ctx, triage, "帮我调研 Go 泛型的最佳实践")
fmt.Println(res.Transfers)   // [researcher]
fmt.Println(res.Agent.Name)  // researcher——最终回答由它产出
```

**嵌套委托（agent-as-tool）**：子任务独立成一次 run，只有最终答案交回上级：

```go
subTool, err := handoff.AsTool(researcher, agent.NewRunner())
triage.Tools = append(triage.Tools, subTool)
```

| 需求 | 用哪个 |
|------|--------|
| 对话要**接管**（用户继续跟新智能体聊） | Handoff |
| 只要**子任务结果**（上级拿着结果继续说话） | AsTool |

**要点**：
- 目标智能体必须有 `Model`（转交校验会拦下 nil）；转交工具与普通工具重名会报错。
- `MaxTurns` 约束**所有**智能体的模型调用总数，防止 A↔B 互相踢皮球。
- 流式下转交有 `StreamHandoff` 事件（`FromAgent`/`ToAgent`）；实现可选的 `agent.HandoffHook` 也能在回调里收到。
- 转交后的 transcript 原样进 session（第 2 课），对话可以在新智能体名下继续。

## 8. 护栏（Guardrails）

**什么时候需要**：用户输入里不该有密钥、模型输出不该带敏感内容。护栏 tripwire（绊线）一触发，整个 run 直接失败。

```go
myAgent.InputGuardrails = []agent.InputGuardrail{
    guardrail.MaxLength(2000), // 输入超长直接拦
    guardrail.DenyPatterns("secrets", regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)),
}
myAgent.OutputGuardrails = []agent.OutputGuardrail{{
    Name: "no-secrets",
    Guardrail: func(ctx context.Context, a *agent.Agent, res *agent.RunResult) (agent.GuardrailResult, error) {
        if strings.Contains(res.Output, "sk-") {
            return agent.GuardrailResult{Tripwire: true, Info: "输出疑似泄露密钥"}, nil
        }
        return agent.GuardrailResult{}, nil
    },
}}

_, err := agent.NewRunner().Run(ctx, myAgent, "帮我看看这个 key 对不对：sk-abc123...")
var trip *agent.GuardrailTripwireError
if errors.As(err, &trip) {
    fmt.Printf("拦截：阶段=%s 护栏=%s 详情=%v\n", trip.Stage, trip.Guardrail, trip.Info)
}
```

**语义要点**：
- 输入护栏在**第一个模型调用之前**并发执行且全部跑完（tripwire 或护栏自身出错都让 run 失败——fail-closed，宁可拒绝也不放行）。trip 时**没有任何模型调用**，不花 token。
- 输出护栏挂在**产出最终回答的智能体**上（转交后就是 specialist 的），在结果发布前执行；流式下文本增量可能已被消费，终态以 `StreamRunError` 为准。
- `guardrail.DenyPatterns` 的 `Info` 只记录 pattern 与命中位置，**不回显命中内容**——避免把真实密钥复制进日志。自己写护栏请遵守同样的纪律。

**模型审核护栏**：接一个审核模型当护栏（包文档有完整示例）：

```go
moderator := model.WithTimeout(openai.New(moderatorCfg), 5*time.Second) // 一定加超时，别让护栏拖死整个 run
myAgent.InputGuardrails = append(myAgent.InputGuardrails, agent.InputGuardrail{
    Name: "moderation",
    Guardrail: func(ctx context.Context, a *agent.Agent, input string) (agent.GuardrailResult, error) {
        res, err := moderator.Chat(ctx, &model.Request{Messages: []model.Message{
            {Role: model.RoleUser, Content: "判断以下内容是否违规，只答 yes/no：" + input},
        }})
        if err != nil {
            return agent.GuardrailResult{}, err // 护栏出错 = fail-closed
        }
        return agent.GuardrailResult{Tripwire: strings.HasPrefix(res.Message.Content, "yes")}, nil
    },
})
```

## 9. 可观测（Hooks、审计、追踪）

**什么时候需要**：出了问题要能回放；合规要留痕。

SDK 的观测分三层，按需取用：

```go
f, err := os.OpenFile("audit.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
if err != nil {
    log.Fatal(err)
}

r := &agent.Runner{
    // 一行接入的结构化日志：只记标识与大小，不记原文与密钥
    Hooks: agent.SlogHooks(slog.Default()),
    // 追踪：为 run、每次模型调用、每次工具调用建 span
    Tracer: tracing.NewSlog(slog.Default()),
}

// 全量审计：与 SlogHooks 并存，扇出到多个 Hooks
auditLog := audit.NewSlog(slog.New(slog.NewJSONHandler(f, nil)))
r.Hooks = agent.MultiHooks(agent.SlogHooks(slog.Default()), auditLog)
```

| 层 | 实现 | 记什么 | 适用 |
|----|------|--------|------|
| 运营日志 | `agent.SlogHooks` | 事件类型、agent 名、消息条数/大小 | 日常排障，日志可外发 |
| 全量审计 | `audit.NewSlog` | 输入/完整 messages/模型输出/工具参数与结果/护栏裁决/转交/用量，全部**原文**，`run_id` 关联 | 合规留痕、事故回放 |
| 追踪 | `tracing.Tracer` | span 层级（run → model.chat → tool.*） | 接 OTel 等分布式追踪 |

**要点**：
- audit 是全量原文——含用户输入与密钥风险，必须写到受保护的 sink（权限 0600 的文件、专用的日志服务），不要进公开日志。
- 自定义 Hooks 只需实现 `agent.Hooks` 接口；`HandoffHook`/`GuardrailHook` 是可选扩展接口，不实现也不影响组合。

## 10. 接入 MCP 服务器

**什么时候需要**：想复用 MCP 生态里现成的工具（文件系统、浏览器、内部服务……），不想自己写适配。

```go
c, err := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportStdio, // 以子进程启动 MCP server
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-everything"},
    Policy:    tool.Denylist("write_file"), // 可选：调用前授权
})
if err != nil {
    log.Fatal(err)
}
if err := c.Connect(ctx); err != nil { // 启动子进程 + MCP 握手
    log.Fatal(err)
}
defer c.Close() // 终止子进程

tools, err := c.Tools(ctx) // []tool.Tool，远端工具已适配好 schema 与结果展平
myAgent.Tools = append(myAgent.Tools, tools...)

res, err := agent.NewRunner().Run(ctx, myAgent, "用工具算一下 40 + 2")
```

远程 MCP 服务走 HTTP：

```go
c, err := mcp.NewClient(mcp.Config{
    Transport: mcp.TransportHTTP,
    URL:       "https://mcp.example.com/mcp",
})
```

**语义要点**：
- 远端工具的 inputSchema 原样透传给模型；结果展平为文本（二进制以尺寸占位）；服务端报 `IsError` 时映射为工具错误回填给模型。
- `Config.Policy` 对**每次**远端调用做授权（复用 `tool.Policy`）：拒绝时不发请求，拒绝原因回填给模型。要人工审批就实现自定义 `Policy`（回调里弹确认框/等审批系统）。
- 权限拒绝不会让 run 失败——它进 `res.ToolErrors`，模型会收到"被拒绝及原因"。

## 11. 韧性（重试、超时、限流、降级）

**什么时候需要**：生产环境一定会有 429、5xx、超时和抖动。

所有模型侧失败都是类型化的 `*model.ModelError`，先看分类再决定策略：

```go
_, err := agent.NewRunner().Run(ctx, myAgent, "hi")
var me *model.ModelError
if errors.As(err, &me) {
    // me.Kind: ErrorRateLimited / ErrorServerError / ErrorTimeout / ErrorNetwork /
    //          ErrorAuth / ErrorInvalidRequest / ErrorProtocol
    // me.Retryable: 该类错误是否值得重试
    // me.StatusCode / me.Provider / me.Body: 定位细节
}
```

韧性中间件都是 `model.Model` 装饰器，自由组合：

```go
primary := model.WithRateLimit(                       // 限流：惰性令牌桶，无后台 goroutine
    model.WithRetry(                                  // 重试：指数退避 + 抖动，只重试可重试类
        model.WithTimeout(openai.New(cfg), 60*time.Second), // 单次调用超时
        model.DefaultRetryPolicy(),
    ),
    5, 5, // 每秒 5 个请求，突发 5
)
backup := model.WithRetry(
    model.WithTimeout(openai.New(backupCfg), 60*time.Second),
    model.DefaultRetryPolicy(),
)
m := model.Fallback(primary, backup) // 降级链：可重试类失败自动切备用模型

myAgent.Model = m
```

**要点**：
- `context.Canceled`（用户取消）永远原样透传，**不会被误重试**。
- 降级链只在"换后端有用"的错误上切换（429/5xx/超时/网络）；`invalid_request` / `protocol` 类错误直接失败——换个后端也一样错。
- Runner 侧还有两道闸：`Runner.ToolTimeout`（单工具超时）、`Runner.MaxTurns`（循环预算）。

## 12. 测试你的智能体

**什么时候需要**：不想烧 token、不想造真实故障，就要测"模型收到 X 时 agent 是否做 Y"。

`testutil.Scripted` 按剧本回放模型行为，并**录制每次请求**供断言：

```go
m := testutil.NewScripted(
    testutil.ToolCallStep("c1", "get_weather", `{"city":"Beijing"}`),
    testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
        // 在这里断言模型收到了什么（工具结果回填了吗？system 对吗？）
        found := false
        for _, msg := range req.Messages {
            if msg.Role == model.RoleTool && strings.Contains(msg.Content, "晴") {
                found = true
            }
        }
        if !found {
            return nil, errors.New("模型没有收到工具结果")
        }
        return testutil.TextStep("done").Resp, nil
    }},
)

res, err := agent.NewRunner().Run(ctx, &agent.Agent{
    Model: m, Tools: []tool.Tool{weatherTool},
}, "北京天气？")
// res.Output == "done"

t.Logf("模型共收到 %d 次请求", m.Calls())
_ = m.LastRequest() // 最近一次请求，深拷贝，可放心断言
```

流式同样有假模型：

```go
sm := testutil.NewScriptedStream(testutil.StreamStep{
    Deltas: []model.StreamEvent{
        testutil.TextChunk("he"),
        testutil.TextChunk("llo"),
    },
    FinishReason: "stop",
})
// sm 同时实现 model.Model 与 model.StreamModel
run := agent.NewRunner().RunStream(ctx, &agent.Agent{Model: sm}, "hi")
res, err := run.Wait()
```

**要点**：
- 剧本按序回放，耗尽时报 `testutil.ErrScriptExhausted`——这本身就是断言（"不该有第 3 次调用"）。
- 注入故障：`Step.Err`、`testutil.HTTPErrorStep(429, "rate limited")`，配合第 11 课的 `WithRetry` 验证韧性逻辑。
- 持续负载与泄漏检查：`SOAK_ITERS=3000 make soak`（可调轮数），覆盖流式/故障注入/三种会话存储/压缩器的长时间运行。

## 13. 内置安全工具（sandbox）

**什么时候需要**：想让智能体执行命令、读文件——shell 是生产环境里最大的攻击面。函数工具（第 4 课）把执行点留在你的代码里，但没有隔离；MCP 本地服务器（第 10 课）同样跑在你的权限下。内置安全工具把执行点收归 SDK：每条命令在**内核级沙箱**里跑，写仅限工作区、网络禁用，配合 deny 规则与审批构成三层防线。

先建一个沙箱（`sandbox.Auto` 给出安全默认：工作区可写、常见系统路径只读、禁网、默认超时 30 秒）：

```go
sb, err := sandbox.Auto("/path/to/workspace") // 会自动创建目录
if err != nil { log.Fatal(err) }             // 默认 fail-closed：内核不支持就报错
defer sb.Close()
```

再把两个内置工具挂到智能体上：

```go
shell, err := builtins.NewShellTool(builtins.ShellConfig{
    Workspace: "/path/to/workspace",
    Sandbox:   sb, // 必填：拒绝注册无沙箱的 shell
})
reader, err := builtins.NewFileTool(builtins.FileConfig{
    Roots: []string{"/path/to/workspace"}, // 只读，路径逃逸/软链/二进制/超大会被拒
})

a := &agent.Agent{
    Name:  "ops",
    Model: model, // 任意 model.Model
    Tools: []tool.Tool{shell, reader},
}
```

模型这时可以调用 `shell`（参数 `{"command":"..."}`）与 `read_file`（参数 `{"path":"..."}`）。工具参数本身就是命令和路径，所以第 9 课的审计层会**逐字记录**每一次执行的完整命令，零额外代码。

危险命令的拦截分两层。第一层是 deny 规则（默认启用 `builtins.DefaultDenyRules()`：递归删除、mkfs、`curl | sh`、sudo、写 `/etc` 等），命中后返回 `*tool.AuthorizationError`，其文本会作为工具结果回给模型：

```go
_, err := shell.Run(ctx, `{"command":"rm -rf /data"}`)
// err: tool "shell" denied by policy: command matches deny rule "..."
```

第二层是审批：用第 4 课的 `tool.WithPolicy` 包装，人类在环时同步阻塞等待决定：

```go
approved := tool.WithPolicy(shell, tool.PolicyFunc(func(ctx context.Context, call tool.ToolCall) error {
    var args struct{ Command string `json:"command"` }
    json.Unmarshal([]byte(call.Arguments), &args)
    return askHuman(ctx, args.Command) // nil 放行，error 拒绝
}))
```

**要点**：
- **fail-closed 贯穿始终**：`sandbox.Auto` 在 Landlock 不可用的内核上直接报错（Linux ≥ 5.13；禁网需要 ≥ 6.7 的 ABI v4）；`NewShellTool` 沙箱缺失直接拒绝注册。只有显式传 `Config.Lax=true` 才降级，且降级后 `sb.Capabilities()` 如实自报哪些限制真实生效。
- **沙箱是安全边界，deny 规则只是护栏**：规则挡"显而易见的错误"，绕过很容易（`rm` 换名字）；真正的防线是 Landlock——工作区外写、任意网络都被内核拒绝，逃逸测试矩阵（读 `/etc`、软链逃逸、拨号、超时杀树）在 CI 里持续验证。
- **防不住提示注入诱导的合法操作**：模型被诱导在工作区内写恶意文件，沙箱不拦——这类威胁要靠 `WithPolicy` 审批 + 审计兜底。
- **`Sandbox` 是长生命周期对象**（Linux 上每个实例驻留一个受限 spawn 线程），每个智能体/工具建一个，不要每次调用新建；`Close` 会杀掉仍在运行的进程树。
- 平台差异：Linux = Landlock + 进程组杀 + prlimit（可选资源限额）；Windows = Job Object 树杀（尽力隔离，无路径/网络限制）；`Capabilities()` 会告诉你真实生效的位。受限更严的替代路线：把执行放进容器或远程沙箱（如 Firecracker），SDK 侧的 `sandbox` 接口设计便于未来接入。

## 附录 A：架构总览

```
                ┌────────────────────────────────────────┐
                │            agent.Runner                │
                │  循环：调模型 → 执行工具 → 回填结果 → 再调模型  │
                │   hooks + spans + 工具防护 + 会话/压缩     │
                └───────┬──────────────────┬─────────────┘
                        │                  │
              model.Model 接口        tool.Tool 接口
                        │                  │
        ┌───────────────┼──────┐    ┌──────┼─────────┬──────────┐
        │  韧性中间件     │      │    │ tool │ skill   │ handoff  │ mcp
        │ retry/timeout │ openai│   │ 函数  │ SKILL.md│ 转交/    │ stdio/
        │ ratelimit     │ 适配器 │   │ 工具  │         │ as-tool  │ http
        │ fallback      └──────┘    └──────┴─────────┴──────────┘
        └───────────────┘
```

依赖方向严格单向（无循环）：`tool → model`；`skill`/`mcp → tool`；`agent → model + tool + tracing`；`handoff`/`audit`/`guardrail`/`session → agent`；`testutil → model`。

零依赖边界：`model`/`tool`/`agent`/`session`/`tracing`/`testutil` 仅用标准库；两个生产依赖是 `mcp` 包的官方 `modelcontextprotocol/go-sdk` 与 `session` 的 SQLite 存储 `modernc.org/sqlite`（纯 Go，无 CGO）。

## 附录 B：与 openai-agents-python 的语义差异

从 Python 版迁移过来时，这三处是刻意不同的设计：

| 语义 | openai-agents-python | 本 SDK | 为什么 |
|------|---------------------|--------|--------|
| 输入护栏时机 | 与第一轮模型调用**并行**（省一次往返） | **先门禁后调用**：护栏全跑完才发首个请求 | 流式下"先出 token 再报 trip"在安全上不可接受；先门禁换来"trip 必无模型调用"与流式一致性 |
| 会话回写 | 每轮结束后回写 | **成功后**回写，失败 run 不落盘 | 半截对话（护栏拦截、模型报错）不该污染下一轮的上下文 |
| 会话压缩 | ——（python 版无内建） | 视图级压缩：存储无损，只收缩模型视图 | `res.Messages` 即模型真实所见，审计与调试不分裂 |

其余核心概念（Agent/Runner/Handoff/guardrail tripwire/MaxTurns）语义对齐，迁移成本主要在类型系统：Python 的装饰器与运行时注册对应到 Go 的显式结构体字段与接口实现。
