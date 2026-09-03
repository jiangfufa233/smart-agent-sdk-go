GO ?= go

.PHONY: fmt vet lint test race cover bench build check

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
		./agent/... ./model/... ./tool/... ./tracing/... ./handoff/... ./skill/... ./mcp/... ./testutil/...
	$(GO) tool cover -func=coverage.out

bench:
	$(GO) test -run='^$$' -bench=. -benchmem ./...

build:
	$(GO) build ./...

# check runs the same gates CI enforces.
check: vet test race build
