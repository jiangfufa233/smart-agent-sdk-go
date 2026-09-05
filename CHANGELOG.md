# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/) and the
project adheres to [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-09-05

### Added

- Sandboxed built-in security tools. The new `sandbox` package confines
  child processes with kernel primitives — Landlock (path whitelists and
  network denial on Linux 5.13+, applied via a dedicated spawn thread),
  Job Objects on Windows (best-effort tree kill), process groups elsewhere
  — with fail-closed construction, honest capability self-reporting
  (`Capabilities` / `Describe`), timeouts that kill the whole process
  tree, sanitized environments and optional `prlimit` resource limits.
- The new `builtins` package exposes that containment as first-party
  tools: `NewShellTool` (denies destructive commands via `DefaultDenyRules`,
  returns `*tool.AuthorizationError` so the model can adapt) and
  `NewFileTool` (read-only, root-constrained, symlink-escape-proof,
  refuses binary and oversized files). Both refuse to register without a
  sandbox. Approval integrates through the existing `tool.WithPolicy`.
- Escape test matrix (reads outside the workspace, symlink escapes,
  network dials, orphan-free timeout kills, environment sanitization) and
  a goleak test for the sandbox package; cross-compile gate for
  windows/darwin in `make cross` and CI.
- Tutorial lesson 13 ("内置安全工具"), offline example section 10, README
  and CI updates. `golang.org/x/sys` is now a direct dependency.

## [0.2.0] - 2026-09-05

### Added

- Core agent loop (`agent.Agent` / `agent.Runner`) with reflection-based
  function tools and automatic JSON Schema generation.
- Provider-agnostic `model.Model` interface with an OpenAI Chat Completions
  adapter that works against any OpenAI-compatible endpoint (vLLM, Ollama,
  Qwen, ...).
- Error taxonomy (`*model.ModelError` with `Kind` / `Retryable`) and
  resilience middlewares: `WithRetry`, `WithTimeout`, `WithRateLimit`,
  `Fallback`.
- Multimodal message content parts; request settings including
  `tool_choice`, `response_format`, `stop`, `seed`.
- Lifecycle hooks (`agent.Hooks`, `agent.SlogHooks`) and minimal tracing
  interfaces (`tracing.Tracer` / `Span`) with no-op and slog implementations.
- Tool hardening: panic recovery, per-tool timeout, output truncation.
- `testutil.Scripted` fake model for offline testing of agents.
- Contract tests for the OpenAI adapter; CI gates (gofmt, vet, race tests,
  70% coverage on library packages, golangci-lint).
- Handoff (agent-as-tool delegation), SKILL.md skills, MCP interface
  scaffold.
- MCP server integration (`mcp` package, built on the official
  `modelcontextprotocol/go-sdk`): stdio child-process and streamable-http
  transports, initialize handshake, remote tools adapted to `tool.Tool`
  with schema passthrough, result flattening and `IsError` mapping.
- Tool authorization (`tool.Policy`, `tool.WithPolicy`, `Allowlist`,
  `Denylist`, `AllowAll`, custom approval callbacks); denials are typed
  (`*tool.AuthorizationError`), do not execute the tool, and are reported
  back to the model.
- First-class handoffs (`agent.Handoff`, `handoff.New`): each handoff is
  exposed as a `transfer_to_<name>` tool; calling it continues the same run
  with the target agent — tool surface, sampling settings and system prompt
  switch mid-conversation, `RunResult.Agent`/`Transfers` report the path,
  `MaxTurns` bounds model calls across all agents, and `StreamHandoff`
  events plus the optional `agent.HandoffHook` extension keep handoffs
  observable. The nested agent-as-tool pattern (`handoff.AsTool`) remains
  available.
- Structured output: `agent.RunTyped[T]` derives a `json_schema` response
  format from the Go type, injects it without mutating the configured
  agent, tolerates Markdown code fences, and decodes the final answer into
  `TypedResult.Value`; decode failures return a typed
  `*agent.StructuredOutputError` carrying the raw model text.
- Input/output guardrails with tripwire semantics (`Agent.InputGuardrails`
  run concurrently before the first model call; `Agent.OutputGuardrails`
  run on the final agent's result before it is published). Trips fail the
  run with a typed `*agent.GuardrailTripwireError`; guardrail errors are
  fail-closed. Built-ins live in the `guardrail` package (`DenyPatterns`,
  `MaxLength`).
- Full-content audit logging (`audit.NewSlog`): a hooks implementation
  recording raw inputs, complete model messages, tool arguments and
  results, guardrail verdicts, handoffs and usage with `run_id`
  correlation — the compliance counterpart of the privacy-preserving
  `agent.SlogHooks`.
- `agent.MultiHooks` fan-out combiner and the optional `agent.GuardrailHook`
  extension, so operational logs and audit logs can share one Runner.
- Session persistence (`agent.Session` interface with `GetItems` / `AddItems`
  / `Clear` and `Runner.RunWithSession` / `RunStreamWithSession`): history is
  loaded before the run, instructions are prepended, and the run's new
  messages are written back only on success — failed runs never persist, and
  writeback failures fail the run. Implementations in the `session` package:
  `NewInMemory`, `NewFile` (JSONL), and `NewSQLiteStore` (single DB file
  holding many keyed sessions via `store.Get(id)`, powered by the pure-Go
  `modernc.org/sqlite` driver).
- History compression (`agent.HistoryCompressor` + `Runner.Compressor`):
  applied to the request view only — session storage stays lossless and
  `res.Messages` reflects the compressed view. `session.NewSlidingWindow(n)`
  truncates; `session.Summarizer{Model, High, Low}` folds older messages into
  a rolling summary with hysteresis and an incremental-fold cache, so model
  calls scale as (len−High)/(High−Low) instead of once per turn.
- Soak test suite (`soak/soak_test.go`): opt-in sustained-load scenarios
  (agent loops, streaming, concurrent sessions, summarizer compression)
  gated by `SOAK_ITERS`, with goroutine / heap / fd leak checks
  (`make soak`, `make soak-race`).

### Documentation

- Restructured the README as a lean entry point: bilingual intro, one
  complete runnable quick-start, a capability-to-lesson index linking into
  the tutorial, and a compact status table. Architecture details, per-file
  descriptions and internal milestone notes were removed in favor of
  pkg.go.dev and the new tutorial.
- Added a step-by-step bilingual tutorial: `docs/tutorial.md` (Chinese) and
  `docs/tutorial.en.md` (English mirror) — 12 lessons plus architecture and
  openai-agents-python difference appendices.
