# smart-agent-sdk-go

**English** — A production-oriented Go SDK for building AI agents, inspired by
`openai-agents-python`. It provides an agent loop with function tools, MCP
integration (stdio & streamable-http) with per-tool authorization policies,
first-class handoffs and agent-as-tool delegation, typed structured output,
input/output guardrails, audit logging, session persistence with view-level
history compression, resilience middleware (retry / timeout / rate limit /
fallback), lifecycle hooks & tracing, incremental SSE streaming, and sandboxed
built-in tools (kernel-level shell execution and safe file reads). Start with
the [English tutorial](docs/tutorial.en.md); API reference lives on
[pkg.go.dev](https://pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go).

中文 — 一个生产级 Go 智能体开发 SDK，对标 `openai-agents-python`。渐进式教程见
[docs/tutorial.md](docs/tutorial.md)（13 课 + 附录，每课可运行、有语义要点与常见坑）。

## 特性

- **智能体循环**：函数工具自动调度、轮次上限、agent-as-tool 子任务委派
- **多智能体编排**：一等公民 handoff（转交后对话延续）与嵌套委派两种模式
- **会话持久化（session persistence）**：内存 / JSONL 文件 / SQLite 三种存储；成功才回写、失败不落盘
- **历史压缩**：滑动窗口与滚动摘要，视图级压缩、存储无损
- **MCP 接入**：stdio 与 streamable-http 传输，按工具授权策略（白/黑名单）
- **流式输出**：`RunStream` 增量事件流，非流式模型自动降级
- **结构化输出**：`RunTyped[T]` 泛型解码，容错解析
- **护栏（guardrails）**：输入/输出 tripwire 语义，内建长度与模式拒绝，fail-closed
- **可观测**：slog 生命周期钩子、全量审计日志、追踪挂点（`tracing.Tracer`）
- **韧性**：统一错误分类 + 重试 / 超时 / 限流 / 降级中间件
- **内置安全工具**：`shell` / `read_file` 跑在 Landlock 沙箱（Linux，fail-closed + 能力自报）里，写仅限工作区、禁网、deny 规则、超时杀树
- **类型化错误体系**：`*model.ModelError` 统一分类，`errors.As` 可解
- **测试友好**：脚本化假模型（同步/流式）+ opt-in soak 长压测试

## 要求

- Go **1.25+**
- 生产依赖仅三个：官方 [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)（MCP）、
  [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)（纯 Go SQLite，无 CGO）与
  [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys)（沙箱原语）。
  核心包 `model` / `tool` / `agent` / `tracing` 只用标准库。

## 5 分钟上手

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
	m := openai.New(openai.Config{
		APIKey:       os.Getenv("OPENAI_API_KEY"),
		DefaultModel: "gpt-4o-mini", // BaseURL 可指向 vLLM / Ollama / Qwen 等兼容端点
	})

	res, err := agent.NewRunner().Run(context.Background(), &agent.Agent{
		Instructions: "You are a helpful assistant.",
		Model:        m,
	}, "用一句话介绍 Go 的 goroutine。")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Output)
}
```

```bash
export OPENAI_API_KEY=sk-...
go run .
```

`Run` 内部是一个循环：调用模型 → 若模型请求工具则执行工具并回传结果 → 直到产出
最终回答（上限 `Agent.Settings.MaxTurns`）。接下来按需阅读：

| 想做什么 | 教程 |
|---|---|
| 多轮对话、记住上下文 | [第 2 课：多轮会话](docs/tutorial.md#2-多轮会话session) |
| 长对话自动压缩、控制 token | [第 3 课：长对话压缩](docs/tutorial.md#3-长对话压缩) |
| 让模型调用你的 Go 函数 | [第 4 课：函数工具](docs/tutorial.md#4-函数工具) |
| 边生成边输出（SSE） | [第 5 课：流式输出](docs/tutorial.md#5-流式输出) |
| 拿到类型化的 JSON 结果 | [第 6 课：结构化输出](docs/tutorial.md#6-结构化输出) |
| 多智能体协作与转交 | [第 7 课：智能体编排](docs/tutorial.md#7-智能体编排handoff) |
| 输入输出安检、内容审核 | [第 8 课：护栏](docs/tutorial.md#8-护栏guardrails) |
| 日志、审计、追踪 | [第 9 课：可观测](docs/tutorial.md#9-可观测hooks审计追踪) |
| 接入 MCP 服务器 | [第 10 课：接入 MCP](docs/tutorial.md#10-接入-mcp-服务器) |
| 重试 / 超时 / 限流 / 降级 | [第 11 课：韧性](docs/tutorial.md#11-韧性重试超时限流降级) |
| 测试你的智能体 | [第 12 课：测试](docs/tutorial.md#12-测试你的智能体) |
| 沙箱里执行命令、读文件 | [第 13 课：内置安全工具](docs/tutorial.md#13-内置安全工具sandbox) |

英文版教程：[docs/tutorial.en.md](docs/tutorial.en.md)。

## 示例

| 示例 | 说明 |
|---|---|
| [`examples/chat`](examples/chat) | 多轮对话 + 会话持久化 |
| [`examples/tools`](examples/tools) | 函数工具定义与调用循环 |
| [`examples/mcp`](examples/mcp) | stdio 方式接入 MCP 服务器 |
| [`examples/offline`](examples/offline) | 无网络端到端冒烟测试（脚本化假模型） |

## 文档

- 教程：[docs/tutorial.md](docs/tutorial.md)（中文） · [docs/tutorial.en.md](docs/tutorial.en.md)（English）
- API 参考：[pkg.go.dev](https://pkg.go.dev/github.com/jiangfufa233/smart-agent-sdk-go)
- 变更记录：[CHANGELOG.md](CHANGELOG.md) · 贡献指南：[CONTRIBUTING.md](CONTRIBUTING.md) · 安全策略：[SECURITY.md](SECURITY.md)

## 状态

| 能力 | 状态 |
|---|---|
| 智能体循环 / 函数工具 / 会话持久化 / 历史压缩 | ✅ |
| 流式输出 / 结构化输出 / Handoff 编排 | ✅ |
| 护栏 + 审计 / 生命周期钩子 / 追踪 | ✅ |
| MCP（stdio / streamable-http）+ 按工具授权 | ✅ |
| 韧性中间件（重试 / 超时 / 限流 / 降级） | ✅ |
| 内置安全工具（Landlock 沙箱 shell / 只读文件工具） | ✅（Linux 全量；Windows/macOS 进程树兜底） |
| Soak 长压测试（opt-in） | ✅ |
| 更多 provider 适配器、示例与教程扩充 | 持续进行 |

## License

[MIT](LICENSE)
