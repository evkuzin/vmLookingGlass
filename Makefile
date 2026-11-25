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
# NPM_REGISTRY redirects the ui stage at an npm mirror. Omit it for the public
# registry.
NPM_ARG = $(if $(NPM_REGISTRY),--build-arg NPM_REGISTRY=$(NPM_REGISTRY))

# The container tool, and flags every invocation of it gets (podman needs
# --cert-dir to trust the Harbor proxy from some hosts, docker needs none).
DOCKER ?= docker
DOCKER_FLAGS ?=

IMAGE ?= harbor.hudson-trading.com/database/vmlookingglass
# An untagged tree yields the short sha, a dirty one says so; a release passes
# VERSION=0.1.0 explicitly. The fallback covers a tree without .git.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build build-host image push test race lint clean help

all: build

# Shared by the build and image recipes. The goproxy URL is a credential, so
# it is read from the environment ($$GOPROXY_URL) rather than expanded: it
# must never appear in a command make echoes (make -n prints even @-prefixed
# lines). It is staged through a 0600 temp file, passed as the `goproxy`
# secret and removed even when the build fails; the trap is installed before
# mktemp, so a failure between mktemp and the build cannot leave the token
# file behind. The docker build arguments are $(1).
define CONTAINER_BUILD
set -e; \
f=''; \
trap '[ -n "$$f" ] && rm -f "$$f" || true' EXIT; \
secrets='$(CA_SECRET)'; \
if [ -n "$$GOPROXY_URL" ]; then \
  f=`mktemp`; printf '%s\n' "$$GOPROXY_URL" > "$$f"; \
  secrets="$$secrets --secret id=goproxy,src=$$f"; \
fi; \
DOCKER_BUILDKIT=1 $(DOCKER) build $(DOCKER_FLAGS) $$secrets $(NPM_ARG) $(1)
endef

## build: build the UI and binaries in a container, leaving them in bin/
#
# The build runs in a container because the UI needs a Node toolchain whose
# version matters, and the host's is shared and can shift underneath us.
# BuildKit writes the binaries straight out to bin/, so no image is kept.
build:
	@$(call CONTAINER_BUILD,--target export --output type=local$(comma)dest=$(BIN) .)
	@echo "binaries with the embedded UI are in $(BIN)/"

## build-host: compile on the host without the UI, for quick daemon iteration
build-host:
	$(GO) build -o $(BIN)/vmlookingglass ./cmd/vmlookingglass
	$(GO) build -o $(BIN)/vmlg ./cmd/vmlg
	@echo "host build has no embedded UI; use 'make build' for a full binary"

## image: build the runnable image as $(IMAGE):$(VERSION)
image:
	@$(call CONTAINER_BUILD,--target runtime -t $(IMAGE):$(VERSION) .)

## push: push the runnable image to its registry
push: image
	$(DOCKER) push $(DOCKER_FLAGS) $(IMAGE):$(VERSION)

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
