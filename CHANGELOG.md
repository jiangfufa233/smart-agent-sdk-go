# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/) and the
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
