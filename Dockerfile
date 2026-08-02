# Builds the UI and the daemon in one pass, so neither depends on what is
# installed on the host. The UI is compiled to static assets and embedded into
# the binary, which is why the Node stage has to run before the Go stage.
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
COPY web/ui/package.json web/ui/package-lock.json* ./
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then \
      npm config set cafile /run/secrets/build_ca; \
    fi && \
    npm ci --no-audit --no-fund || npm install --no-audit --no-fund

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


FROM alpine:3.21

# ca-certificates is installed from the package rather than by appending to the
# bundle: installing it regenerates the bundle from the cert directories, which
# would drop an appended corporate CA, so the runtime image ships public CAs
# only.
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then cat /run/secrets/build_ca >> /etc/ssl/certs/ca-certificates.crt; fi && \
    apk --no-cache add ca-certificates && \
    addgroup -g 1000 vmlg && \
    adduser -D -u 1000 -G vmlg vmlg && \
    mkdir -p /etc/vmlookingglass && chown vmlg:vmlg /etc/vmlookingglass

COPY --from=builder /out/vmlookingglass /out/vmlg /usr/bin/

USER vmlg
EXPOSE 8880

ENTRYPOINT ["/usr/bin/vmlookingglass"]
CMD ["-config", "/etc/vmlookingglass/config.yml"]
