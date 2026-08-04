# Builds the UI and the daemon in one pass, so neither depends on what is
# installed on the host. The UI is compiled to static assets and embedded into
# the binary, which is why the Node stage has to run before the Go stage.
#
# Two final stages: `export` emits just the binaries for --output, and
# `runtime` is a runnable image. A plain `docker build` with no --target
# builds the last stage, so `runtime` is deliberately last.
#
# Outbound HTTPS is TLS-inspected with a cert chained to the private root CAs,
# which these base images do not trust. The bundle is passed as a BuildKit
# secret so nothing host-specific is committed, and each guard keeps the build
# working off-network when no secret is supplied.

FROM node:24-alpine AS ui

RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then cat /run/secrets/build_ca >> /etc/ssl/certs/ca-certificates.crt; fi

WORKDIR /ui

# Dependencies are installed from the manifests alone, so editing source does
# not invalidate the install layer.
COPY web/ui/package.json web/ui/package-lock.json ./
# The trust bundle is exported only when a secret was actually supplied, so
# an ordinary build never sets it. NODE_EXTRA_CA_CERTS is read at startup and
# lives only for this step, unlike `npm config set cafile`, which would
# persist a path that stops existing once the secret is unmounted.
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then \
      export NODE_EXTRA_CA_CERTS=/run/secrets/build_ca; \
    fi; \
    npm ci --no-audit --no-fund

COPY web/ui/ ./
RUN npm run build


FROM golang:1.26-alpine AS builder

RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then cat /run/secrets/build_ca >> /etc/ssl/certs/ca-certificates.crt; fi

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The built UI replaces the placeholder the repo commits, so go:embed picks up
# real assets rather than an empty directory.
COPY --from=ui /ui/dist ./web/ui/dist

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags='-s -w' -o /out/vmlookingglass ./cmd/vmlookingglass && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags='-s -w' -o /out/vmlg ./cmd/vmlg


# Holds only the binaries. BuildKit writes this straight to the host with
# --output, so `make build` produces the binaries without keeping an image.
FROM scratch AS export
COPY --from=builder /out/vmlookingglass /out/vmlg /


# A runnable image, for deployments that want a container rather than a
# binary. This is the last stage, so a plain `docker build` produces it.
FROM alpine:3.21 AS runtime

# ca-certificates is installed from the package rather than by appending to the
# bundle: installing it regenerates the bundle from the cert directories, which
# would drop an appended corporate CA, so the runtime image ships public CAs
# only.
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then cat /run/secrets/build_ca >> /etc/ssl/certs/ca-certificates.crt; fi && \
    apk --no-cache add ca-certificates && \
    addgroup -g 1000 vmlg && \
    adduser -D -u 1000 -G vmlg vmlg && \
    mkdir -p /etc/vmLookingGlass && chown vmlg:vmlg /etc/vmLookingGlass

COPY --from=builder /out/vmlookingglass /out/vmlg /usr/bin/

USER vmlg
EXPOSE 8880

ENTRYPOINT ["/usr/bin/vmlookingglass"]
CMD ["-config", "/etc/vmLookingGlass/config.yml"]
