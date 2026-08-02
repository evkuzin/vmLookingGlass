BIN := bin

GO ?= go

# A literal comma cannot appear directly inside $(if ...): make reads it as an
# argument separator.
comma := ,
# BUILD_CA points at the corporate trust bundle, needed while the build fetches
# through TLS interception. Omit it to build off-network.
CA_SECRET = $(if $(BUILD_CA),--secret id=build_ca$(comma)src=$(BUILD_CA))

.PHONY: all build ui image extract test race lint clean help

all: build

## build: compile the daemon and the CLI into bin/
build:
	$(GO) build -o $(BIN)/vmlookingglass ./cmd/vmlookingglass
	$(GO) build -o $(BIN)/vmlg ./cmd/vmlg

## ui: build the web UI into web/ui/dist (needs npm)
ui:
	cd web/ui && npm install && npm run build

## image: build UI and binaries in a container, avoiding host toolchains
image:
	DOCKER_BUILDKIT=1 docker build $(CA_SECRET) \
	  -t vmlookingglass:$(or $(TAG),dev) .

## extract: copy the binaries out of the image into bin/
extract: image
	@mkdir -p $(BIN)
	$(eval CID := $(shell docker create vmlookingglass:$(or $(TAG),dev)))
	@docker cp $(CID):/usr/bin/vmlookingglass $(BIN)/vmlookingglass
	@docker cp $(CID):/usr/bin/vmlg $(BIN)/vmlg
	@docker rm -v $(CID) >/dev/null
	@echo "binaries with the embedded UI are in $(BIN)/"

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
