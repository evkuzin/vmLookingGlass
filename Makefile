BIN := bin

GO ?= go

# Local settings, so the corporate CA and similar do not have to be passed on
# every invocation. Optional: the build works without it off-network.
# -include tolerates the file being absent.
-include .env
export

# A literal comma cannot appear directly inside $(if ...): make reads it as an
# argument separator.
comma := ,
# BUILD_CA points at the corporate trust bundle, needed while the build fetches
# through TLS interception. Omit it to build off-network.
CA_SECRET = $(if $(BUILD_CA),--secret id=build_ca$(comma)src=$(BUILD_CA))

.PHONY: all build build-host test race lint clean help

all: build

## build: build the UI and binaries in a container, leaving them in bin/
#
# The build runs in a container because the UI needs a Node toolchain whose
# version matters, and the host's is shared and can shift underneath us.
# BuildKit writes the binaries straight out to bin/, so no image is kept.
build:
	DOCKER_BUILDKIT=1 docker build $(CA_SECRET) --target export \
	  --output type=local$(comma)dest=$(BIN) .
	@echo "binaries with the embedded UI are in $(BIN)/"

## build-host: compile on the host without the UI, for quick daemon iteration
build-host:
	$(GO) build -o $(BIN)/vmlookingglass ./cmd/vmlookingglass
	$(GO) build -o $(BIN)/vmlg ./cmd/vmlg
	@echo "host build has no embedded UI; use 'make build' for a full binary"

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
