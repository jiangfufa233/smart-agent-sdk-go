# agent-sdk（Go 版智能体开发 SDK）

一个受 `openai-agents-python` 启发的轻量级 Go 智能体 SDK，提供基本的多轮对话、函数工具（Tools）、MCP 与 Skills 接入骨架，以及基于"agent-as-tool"模式的智能体编排能力。

- **零外部依赖**：仅使用 Go 标准库。
- **Go 版本要求**：`go 1.24`+（见 `go.mod`）。
- **模块路径**：`github.com/example/agent-sdk`（可按需改名）。

## 目录结构

```
agent-sdk/
├── go.mod                  # 模块定义，无 require 条目（零依赖）
├── README.md               # 本文档
├── model/                  # 模型抽象层
│   ├── model.go            # 消息/请求/响应类型 + Model 接口
│   └── openai/
│       └── openai.go       # OpenAI Chat Completions 适配器
├── tool/                   # 工具运行时
│   ├── tool.go             # Tool 接口 + 反射版函数工具适配器
│   └── schema.go           # 反射生成 JSON Schema
├── agent/                  # 智能体与执行循环
│   ├── agent.go            # Agent 配置类型
│   └── runner.go           # Runner 工具调用循环
├── handoff/
│   └── handoff.go          # 智能体间委托（agent-as-tool）
├── skill/
│   └── skill.go            # SKILL.md 技能加载器
├── mcp/
│   └── mcp.go              # MCP 客户端接口骨架（未实现）
└── examples/               # 可运行示例
    ├── chat/main.go        # 多轮终端对话
    ├── tools/main.go       # 函数工具调用
    └── offline/main.go     # 无网络冒烟测试（脚本化假模型）
```

## 架构总览

```
                ┌────────────────────────────────────────┐
                │            agent.Runner                │
                │  循环：调模型 → 执行工具 → 回填结果 → 再调模型  │
                └───────┬──────────────────┬─────────────┘
                        │                  │
              model.Model 接口        tool.Tool 接口
                        │                  │
            ┌───────────┴────┐    ┌────────┼─────────┬──────────┐
            │ model/openai   │    │ tool   │ skill   │ handoff  │ mcp
            │ Chat Completions│   │ 函数工具│ SKILL.md│ agent-as-│ (stub)
            └────────────────┘    └────────┘  渐进披露 │  tool    │
                                                      └──────────┘
```

依赖方向严格单向，避免循环依赖：

- `tool` → `model`（工具规格使用 `model.ToolParam`）
- `skill`、`mcp` → `tool`（把外部能力适配为 `tool.Tool`）
- `agent` → `model` + `tool`
- `handoff` → `agent` + `tool`
- `examples/*` → 以上全部

### 核心执行流程（Runner 循环）

1. 组装消息：`system`（Agent 指令）+ 历史消息 + 本轮 `user` 输入；
2. 携带所有工具定义调用 `Model.Chat`；
3. 若模型返回 `tool_calls`：逐个执行对应 `tool.Tool.Run`，把结果以 `role=tool` 消息回填，回到第 2 步；
4. 若模型直接返回文本：作为最终答案结束；
5. 超过 `Runner.MaxTurns`（默认 10）返回 `agent.ErrMaxTurns`。

## 包与文件说明

### `model/` —— 模型抽象层

| 文件 | 作用 |
|------|------|
| `model.go` | 定义与具体厂商无关的类型：`Role`、`Message`、`ToolCall`/`FunctionCall`、`ToolParam`/`FunctionDef`（JSON Schema 挂在 `Parameters` 上）、`Request`、`Response`、`Usage`；以及核心接口 `Model { Chat(ctx, *Request) (*Response, error) }`。类型字段与 OpenAI Chat Completions 的 wire format 对齐，便于适配器直接序列化。 |
| `openai/openai.go` | OpenAI Chat Completions 的标准库实现：`Config{APIKey, BaseURL, DefaultModel, HTTPClient}` → `Client`。发送 `POST {BaseURL}/chat/completions`，处理错误状态码并解析 `choices/usage`。**`BaseURL` 可指向任意 OpenAI 兼容端点**（vLLM、Ollama、Qwen 等），无需改代码。 |

### `tool/` —— 工具运行时

| 文件 | 作用 |
|------|------|
| `tool.go` | 定义 `Tool` 接口（`Spec()` 返回工具定义、`Run(ctx, argumentsJSON)` 执行并返回文本结果）和 `FunctionTool`。`NewFunction(name, desc, fn)` 通过反射校验函数签名：支持 `func(in Args) (T, error)` 或带前置 `context.Context` 的变体；入参必须是 struct；返回值为 `string` 时原样返回，其他类型 JSON 序列化。 |
| `schema.go` | `SchemaFromType(reflect.Type)` 递归生成 JSON Schema：string/bool/整型/浮点/数组/映射/结构体；识别 `json` 标签（命名与 `omitempty` 决定 required）、自定义 `desc` 标签生成字段描述；`time.Time` 映射为 `{"type":"string","format":"date-time"}`。 |

### `agent/` —— 智能体与执行循环

| 文件 | 作用 |
|------|------|
| `agent.go` | `Agent` 配置类型：`Name`、`Instructions`（系统提示）、`Model`（实现 `model.Model` 的提供商）、`ModelName`（模型标识，如 `gpt-4o-mini`）、`Tools`。 |
| `runner.go` | `Runner{MaxTurns}` 执行核心循环（见上图）。`Run` 开启新会话；`RunWithHistory` 在既有消息历史（可复用上一次 `RunResult.Messages`）上继续，支持多轮对话。`RunResult` 含最终答案 `Output`、`FinalMessage` 与完整 `Messages`。哨兵错误 `ErrMaxTurns`。 |

### `handoff/` —— 智能体编排

| 文件 | 作用 |
|------|------|
| `handoff.go` | `AsTool(target *agent.Agent, r *agent.Runner)` 把目标智能体包装成名为 `transfer_to_<name>` 的工具，实现 supervisor → 子智能体委托（agent-as-tool 模式）。子智能体在自己的独立会话中运行并把最终答案返回给调用方。 |

### `skill/` —— 技能加载

| 文件 | 作用 |
|------|------|
| `skill.go` | 加载 `SKILL.md` 技能清单：`LoadDir` 支持"目录下直接放 SKILL.md"与"每个子目录一个 SKILL.md"两种布局；`LoadFile` 解析 `---` 分隔的极简 frontmatter（`name`/`description`）；`Skill.Tool()` 以**渐进披露**方式暴露技能——模型平时只看到名称与描述，调用工具时才返回完整正文。 |

### `mcp/` —— MCP 接入（骨架）

| 文件 | 作用 |
|------|------|
| `mcp.go` | 仅定义公开接口面：`Transport`（`stdio` / `streamable-http`）、`Config`、`Client`。`Connect`/`Tools` 当前返回 `ErrNotImplemented`。后续计划基于官方 `github.com/modelcontextprotocol/go-sdk` 实现，并把远端工具适配为 `tool.Tool`。 |

### `examples/` —— 示例

| 示例 | 运行方式 | 说明 |
|------|----------|------|
| `examples/chat` | `OPENAI_API_KEY=sk-... go run ./examples/chat` | 终端多轮对话：用 `RunWithHistory` 累积历史消息。 |
| `examples/tools` | `OPENAI_API_KEY=sk-... go run ./examples/tools` | 定义 `weatherArgs` 结构体 + 天气函数，演示反射生成 schema、模型发起工具调用并得到最终回答。 |
| `examples/offline` | `go run ./examples/offline` | **无需网络与 API Key**：用脚本化假模型完整跑通 schema 生成、工具循环、handoff 与 skill 加载，兼作冒烟测试。 |

## 扩展指南

**接入新的模型提供商**：实现 `model.Model` 接口（一个 `Chat` 方法）即可传给 `agent.Agent`；若提供商是 OpenAI 兼容协议，直接用 `openai.Config{BaseURL: ...}`。

**新增工具**：写一个结构体入参的函数，用 `tool.NewFunction` 包装：

```go
type searchArgs struct {
    Query string `json:"query" desc:"搜索关键词"`
}
t, err := tool.NewFunction("search", "搜索内部知识库",
    func(ctx context.Context, in searchArgs) (string, error) { ... })
agent.Tools = append(agent.Tools, t)
```

**新增技能**：在目录中放 `SKILL.md`（含 `name`/`description` frontmatter），`skill.LoadDir` 加载后逐个 `Skill.Tool()` 挂到智能体上。

**编排子智能体**：`handoff.AsTool(subAgent, nil)` 得到委托工具，加入主智能体的 `Tools`。

## 当前状态与路线图

| 能力 | 状态 |
|------|------|
| 多轮对话 / Runner 循环 | ✅ 可用 |
| 函数工具 + 反射 JSON Schema | ✅ 可用 |
| Skills（SKILL.md 加载 + 渐进披露） | ✅ 基本可用 |
| 智能体编排（agent-as-tool） | ✅ 基本可用 |
| MCP 客户端 | ⬜ 接口已定义，待实现 |
| 流式输出（SSE） | ⬜ 未实现 |
| Guardrails / Hooks / 持久化会话 / 追踪 | ⬜ 规划中 |

## 验证

```bash
gofmt -l .          # 无输出即格式合规
go build ./...      # 编译所有包
go vet ./...        # 静态检查
go run ./examples/offline   # 无网络端到端冒烟测试
```
