GO ?= go

.PHONY: fmt vet lint test race cover bench build cross soak soak-race check

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

test:
	$(GO) test -count=1 ./...

race:
	$(GO) test -race -count=1 ./...

cover:
	$(GO) test -race -count=1 -covermode=atomic -coverprofile=coverage.out \
		./agent/... ./model/... ./tool/... ./tracing/... ./handoff/... ./skill/... ./mcp/... ./testutil/... ./audit/... ./guardrail/... ./session/... ./sandbox/... ./builtins/...
	$(GO) tool cover -func=coverage.out

bench:
	$(GO) test -run='^$$' -bench=. -benchmem ./...

build:
	$(GO) build ./...

# cross proves the platform-specific sandbox backends (Landlock, Job
# Objects, process groups) compile on every supported OS.
cross:
	GOOS=windows $(GO) build ./...
	GOOS=darwin $(GO) build ./...

# Soak: opt-in sustained-load scenarios (streaming/handoffs, fault
# injection, session stores, compressors) with goroutine/heap/fd leak
# checks. Override ITERS for longer runs, e.g. make soak ITERS=20000.
soak:
	SOAK_ITERS=$(ITERS) $(GO) test -count=1 ./soak

soak-race:
	SOAK_ITERS=$(ITERS) $(GO) test -race -count=1 ./soak

# check runs the same gates CI enforces.
check: vet test race build cross
