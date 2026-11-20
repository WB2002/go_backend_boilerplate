# syntax=docker/dockerfile:1.7

# ------------------------------------------------------------------------------
# Stage 1: Build Environment
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.23.3-alpine3.20@sha256:c694a4d291a13a9f9d94933395673494fc2cc9d4777b85df3a7e70b3492d3574 AS builder

# Install required build utilities and SSL root certificates
RUN apk add --no-cache ca-certificates tzdata

# Create an unprivileged system user for runtime execution
RUN adduser -D -g '' -u 10001 -h /home/appuser appuser

WORKDIR /src

# Leverage Docker layer caching for module dependencies
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source tree
COPY . .

# Target platform arguments passed by Docker BuildKit
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none

# Compile statically linked, stripped, reproducible binaries using BuildKit caches
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-arm64} \
    go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/api ./cmd/api

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-arm64} \
    go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/worker ./cmd/worker

# ------------------------------------------------------------------------------
# Stage 2: Shared Minimal Runtime Base
# ------------------------------------------------------------------------------
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS base-runtime

# ca-certificates for outbound TLS, tzdata for timezones, wget for healthcheck
RUN apk add --no-cache ca-certificates tzdata wget && \
    rm -rf /var/cache/apk/*

# Copy unprivileged user from builder
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# Invariant check: enforce zero markdown files in the final runtime container
RUN find / -name "*.md" 2>/dev/null | (! grep .)

WORKDIR /app

# ------------------------------------------------------------------------------
# Target: api (Core HTTP / GraphQL / WebSocket Server)
# ------------------------------------------------------------------------------
FROM base-runtime AS api

COPY --from=builder /out/api /usr/local/bin/api
COPY --from=builder --chown=appuser:appuser /home/appuser /home/appuser

# Run as non-root user
USER appuser

# Expose default HTTP server port
EXPOSE 8080

# Container liveness check targeting lightweight /health/live probe
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/health/live || exit 1

ENTRYPOINT ["/usr/local/bin/api"]

# ------------------------------------------------------------------------------
# Target: worker (Background Jobs Engine Worker)
# ------------------------------------------------------------------------------
FROM base-runtime AS worker

COPY --from=builder /out/worker /usr/local/bin/worker
COPY --from=builder --chown=appuser:appuser /home/appuser /home/appuser

# Run as non-root user
USER appuser

ENTRYPOINT ["/usr/local/bin/worker"]
