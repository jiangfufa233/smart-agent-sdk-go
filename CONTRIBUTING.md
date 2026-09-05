# Contributing

Thanks for considering a contribution.

## Development

```bash
go build ./...        # compile
make check            # vet + test + race + build (same gates as CI)
make cover            # coverage report over library packages
make bench            # benchmarks
make soak             # opt-in sustained-load tests (set SOAK_ITERS, default off)
go run ./examples/offline   # offline end-to-end smoke test
```

CI enforces: `gofmt`, `go vet`, `go test -race`, 70% coverage on library
packages, and `golangci-lint` (config in `.golangci.yml`).

## Guidelines

- The types in `model` are the wire compatibility surface: additive changes
  only. If you need a new field, add it; do not repurpose existing ones.
- Provider adapters must return `*model.ModelError` (use
  `model.ClassifyHTTPStatus` / `model.ClassifyTransportError`) so retry,
  rate-limit and fallback middlewares classify failures correctly.
- Every exported identifier needs a doc comment; new features need tests and
  a runnable example or an entry in `examples/`.
- No background goroutines in library code; verify with the goleak guard in
  `agent/runner_test.go`.
- Keep the core (`model`, `tool`, `agent`, `tracing`) on the standard
  library only.

## Reporting issues

Bug reports and feature requests go through GitHub Issues. Security issues
follow [SECURITY.md](SECURITY.md) — please do not open public issues for
them.
