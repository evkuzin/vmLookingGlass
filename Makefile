BIN := bin

GO ?= go

.PHONY: all build test race lint clean help

all: build

## build: compile the daemon and the CLI into bin/
build:
	$(GO) build -o $(BIN)/vmlookingglass ./cmd/vmlookingglass
	$(GO) build -o $(BIN)/vmlg ./cmd/vmlg

## test: run the test suite
test:
	$(GO) test ./...

## race: run the test suite under the race detector
race:
	$(GO) test -race -timeout 5m ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## clean: remove build output and the test cache
clean:
	rm -rf $(BIN)
	$(GO) clean -testcache

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
