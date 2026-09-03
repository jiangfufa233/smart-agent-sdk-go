# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities through GitHub
[private security advisories](https://docs.github.com/en/code-security/security-advisories)
(Repository → Security → Advisories → New draft security advisory).

Do not open a public issue for a security problem.

## Scope

- Prompt-injection surface: tool results are untrusted text fed back to the
  model; the SDK marks truncation and does not interpret tool output as
  instructions.
- The SDK never logs raw inputs, tool outputs or API keys via
  `agent.SlogHooks` / `tracing.NewSlog` (identifiers and sizes only).
- API keys are only held in provider `Config` values and sent as
  `Authorization` headers; they are never written to errors or traces
  (error bodies are truncated to 2 KiB and come from the provider response).

## Supported versions

Pre-1.0: only the latest tagged release receives security fixes.
