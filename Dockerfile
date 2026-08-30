# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1 — build
# ---------------------------------------------------------------------------
# Must match (or exceed) the `go` directive in go.mod. The container sets
# GOTOOLCHAIN=local, so unlike a local build it will not auto-download a newer
# toolchain — an older base image fails the build outright.
FROM golang:1.25-alpine AS builder

# git is needed for module downloads; ca-certificates so the build can reach
# proxy.golang.org over TLS.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Copy the manifests first so dependency download is cached independently of
# source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is disabled so the result is a fully static binary that runs on a bare
# Alpine image. Symbols and DWARF are stripped to keep it small.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/api \
        ./cmd/api

# ---------------------------------------------------------------------------
# Stage 2 — runtime
# ---------------------------------------------------------------------------
FROM alpine:3.20

# ca-certificates: outbound TLS to Supabase and the AI service.
# tzdata: the scheduler and timestamps operate in UTC.
# wget (busybox): the container healthcheck below.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S cis && adduser -S -G cis cis

WORKDIR /app

COPY --from=builder /out/api /app/api

# Only needed by STORAGE_DRIVER=local. Created up front and owned by the
# non-root user so a dev-mode container can write to it.
RUN mkdir -p /app/uploads && chown -R cis:cis /app

USER cis

ENV APP_PORT=8080 \
    TZ=UTC

EXPOSE 8080

# Uses the liveness probe, which deliberately performs no dependency checks —
# a database blip must not restart an otherwise healthy container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:${APP_PORT}/health || exit 1

ENTRYPOINT ["/app/api"]
