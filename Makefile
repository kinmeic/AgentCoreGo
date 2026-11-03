GO ?= go
BINARY ?= agent-core

.PHONY: build test race vet fmt check clean docker

build:
	$(GO) build -trimpath -o $(BINARY) ./cmd/agent-core

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./cmd/... ./internal/...

check:
	@test -z "$$(gofmt -l ./cmd ./internal)"
	$(GO) vet ./...
	$(GO) test -race ./...
	$(GO) build ./cmd/agent-core

clean:
	rm -f $(BINARY)
	rm -rf dist

docker:
	docker build -t agent-core:local .
