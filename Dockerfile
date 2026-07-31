# meshcraft controller — 100% container build.
# Output: the controller + multi-arch proxy binaries + OCI layout
# for the proxy image served by the integrated registry.

# ---- Frontend (Vite) ----
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Backend Go + multi-arch proxy binaries ----
FROM golang:1.24-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The Vite build replaces the embedded placeholder.
COPY --from=web /web/dist/ internal/api/webdist/

# UTC build timestamp (YYYYMMDD.HHMMSS), passed by the Makefile. THIS is the
# technical proxy version: monotonic, text-comparable, therefore usable to
# decide on an upgrade. The product version (VERSION) only moves on releases.
# See docs/fr/01-Architecture/Versionnage et mise à jour du proxy.
#
# NO DEFAULT, AND THE BUILD FAILS WITHOUT IT. It used to default to "dev", and
# that default cost a stuck proxy: a proxy stamped `dev` is lexicographically
# NEWER than any timestamp (digits sort before letters), so the controller never
# offers an update and reports "up to date" forever. A `docker compose pull` did
# not help — the integrated registry served the same build. Fixing it required
# going to the proxy host — which this architecture forbids.
ARG MESHCRAFT_BUILD
RUN case "$MESHCRAFT_BUILD" in \
      [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]) ;; \
      *) echo "MESHCRAFT_BUILD=\"$MESHCRAFT_BUILD\": a UTC build timestamp YYYYMMDD.HHMMSS is REQUIRED. Build with 'make build', which computes it once and propagates it to all three architectures and the controller. A proxy shipped without a comparable timestamp can no longer be upgraded from the controller (see distrib.IsNewer)." >&2 ; exit 1 ;; \
    esac && \
    test -s VERSION || { echo "VERSION file missing or empty: the product version must not be guessed." >&2 ; exit 1 ; }

# Proxy: static cross-compile amd64 / arm64 / armv7.
# `version` = build timestamp (what the proxy announces and the controller
# compares), `product` = product version, purely informative.
RUN mkdir -p /out/artifacts && \
    for a in amd64 arm64 arm; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$a GOARM=7 \
      go build -trimpath \
        -ldflags="-s -w -X main.version=$MESHCRAFT_BUILD -X main.product=$(cat VERSION)" \
        -o /out/artifacts/meshcraft-proxy_linux_$a ./cmd/proxy ; \
    done && \
    printf '%s' "$MESHCRAFT_BUILD" > /out/artifacts/VERSION

RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=$(cat VERSION) -X main.build=$MESHCRAFT_BUILD" \
      -o /out/controller ./cmd/controller && \
    CGO_ENABLED=0 go build -trimpath -o /out/mkoci ./cmd/mkoci

# Multi-arch OCI image of the proxy (without buildx) for the integrated registry.
RUN /out/mkoci --out /out/oci \
      --cacert /etc/ssl/certs/ca-certificates.crt \
      --bin amd64=/out/artifacts/meshcraft-proxy_linux_amd64 \
      --bin arm64=/out/artifacts/meshcraft-proxy_linux_arm64 \
      --bin arm/v7=/out/artifacts/meshcraft-proxy_linux_arm

# ---- Runtime ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -H meshcraft
COPY --from=build /out/controller /usr/local/bin/controller
COPY --from=build /out/artifacts /srv/meshcraft/artifacts
COPY --from=build /out/oci /srv/meshcraft/oci
USER meshcraft
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
  CMD wget -qO- http://127.0.0.1:8080/api/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/controller"]
