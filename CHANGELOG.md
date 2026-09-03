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
